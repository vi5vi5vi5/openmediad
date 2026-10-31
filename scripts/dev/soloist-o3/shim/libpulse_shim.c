/*
 * libpulse_shim.c — минимальная замена libpulse.so.0 для Spotify Soloist (опыт О3, docs/MUSIC-PLAN.md §10).
 *
 * Soloist на Linux делает dlopen("libpulse.so.0") и берёт 48 функций pa_* (список снят со строк его бинарника).
 * Здесь они реализованы так, чтобы клиент видел «настоящий» сервер: потоковый mainloop, контекст, поток воспроизведения,
 * громкость, пауза, flush, тайминги. Звук (float32 или s16) пересчитывается в S16LE и уходит
 *   — в `aplay` (по умолчанию, запускается на каждое подключение потока), или
 *   — в FIFO/файл (OMD_PCM_FIFO), откуда его заберёт движок openmediad.
 *
 * Код написан по публичному API PulseAudio (заголовки каталога pulse) без заимствований из чужих шимов.
 *
 * Настройки окружения (все необязательные):
 *   OMD_PCM_DEV          устройство aplay (по умолчанию "default")
 *   OMD_PCM_APLAY_ARGS   дополнительные аргументы aplay через пробел (по умолчанию "--buffer-time=150000")
 *   OMD_PCM_CMD          своя команда вместо aplay (через /bin/sh -c; переменные OMD_PCM_RATE, OMD_PCM_CHANNELS)
 *   OMD_PCM_FIFO         писать S16LE в этот файл/FIFO вместо запуска aplay
 *   OMD_PCM_PIPE_BYTES   размер канала до aplay (по умолчанию 16384)
 *   OMD_SHIM_VOLUME      linear (по умолчанию) | cubic (как PulseAudio) | off (громкость Soloist игнорируется)
 *   OMD_SHIM_LAT_US      оценка задержки вывода для timing_info (по умолчанию 250000)
 *   OMD_SHIM_NO_UNDERFLOW=1  не вызывать underflow-callback
 *   OMD_SHIM_DEBUG=1     подробный журнал вызовов в stderr
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <pthread.h>
#include <pulse/pulseaudio.h>
#include <signal.h>
#include <spawn.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/time.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

extern char **environ;

#define SHIM_VERSION "0.1"
#define RING_MS 500    /* буфер потока, мс */
#define CHUNK_FRAMES 2048

/* ---------- журнал ---------- */

static int g_dbg;
static int g_nounderflow;
static int g_volmode; /* 0 linear, 1 cubic, 2 off */
static long g_lat_us = 250000;

static uint64_t now_ms(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return (uint64_t)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}

static void logf_(const char *fmt, ...) {
	char b[512];
	va_list ap;
	va_start(ap, fmt);
	vsnprintf(b, sizeof b, fmt, ap);
	va_end(ap);
	fprintf(stderr, "[shim %llu] %s\n", (unsigned long long)(now_ms() % 1000000), b);
}
#define LOG(...) logf_(__VA_ARGS__)
#define DBG(...) do { if (g_dbg) logf_(__VA_ARGS__); } while (0)

/* ---------- очередь событий mainloop ---------- */

struct ev;
typedef void (*evfn)(struct ev *e);
struct ev {
	struct ev *next;
	evfn fn;
	void *a, *b, *c, *d;
	intptr_t i;
};

struct pa_threaded_mainloop {
	pthread_mutex_t lock;      /* то самое pa_threaded_mainloop_lock (рекурсивный) */
	pthread_cond_t wait_cond;  /* pa_threaded_mainloop_wait / signal */
	pthread_mutex_t qlock;     /* очередь событий; с lock не связана, чтобы писатель не зависел от клиента */
	pthread_cond_t qcond;
	struct ev *qh, *qt;
	pthread_t th;
	int started, quit;
	pa_mainloop_api api;
};

static void ml_post(struct pa_threaded_mainloop *ml, evfn fn, void *a, void *b, void *c, void *d, intptr_t i) {
	struct ev *e = calloc(1, sizeof *e);
	if (!e)
		return;
	e->fn = fn; e->a = a; e->b = b; e->c = c; e->d = d; e->i = i;
	pthread_mutex_lock(&ml->qlock);
	if (ml->qt)
		ml->qt->next = e;
	else
		ml->qh = e;
	ml->qt = e;
	pthread_cond_signal(&ml->qcond);
	pthread_mutex_unlock(&ml->qlock);
}

static void *ml_thread(void *arg) {
	struct pa_threaded_mainloop *ml = arg;
	for (;;) {
		pthread_mutex_lock(&ml->qlock);
		while (!ml->qh && !ml->quit)
			pthread_cond_wait(&ml->qcond, &ml->qlock);
		struct ev *e = ml->qh;
		if (e) {
			ml->qh = e->next;
			if (!ml->qh)
				ml->qt = NULL;
		}
		int stop = ml->quit && !e;
		pthread_mutex_unlock(&ml->qlock);
		if (stop)
			break;
		if (e) {
			pthread_mutex_lock(&ml->lock);
			e->fn(e);
			pthread_mutex_unlock(&ml->lock);
			free(e);
		}
	}
	return NULL;
}

pa_threaded_mainloop *pa_threaded_mainloop_new(void) {
	struct pa_threaded_mainloop *ml = calloc(1, sizeof *ml);
	if (!ml)
		return NULL;
	pthread_mutexattr_t at;
	pthread_mutexattr_init(&at);
	pthread_mutexattr_settype(&at, PTHREAD_MUTEX_RECURSIVE);
	pthread_mutex_init(&ml->lock, &at);
	pthread_mutexattr_destroy(&at);
	pthread_cond_init(&ml->wait_cond, NULL);
	pthread_mutex_init(&ml->qlock, NULL);
	pthread_cond_init(&ml->qcond, NULL);
	ml->api.userdata = ml;
	DBG("pa_threaded_mainloop_new");
	return ml;
}

int pa_threaded_mainloop_start(pa_threaded_mainloop *m) {
	if (m->started)
		return -1;
	m->quit = 0;
	if (pthread_create(&m->th, NULL, ml_thread, m) != 0)
		return -1;
	m->started = 1;
	DBG("pa_threaded_mainloop_start");
	return 0;
}

void pa_threaded_mainloop_stop(pa_threaded_mainloop *m) {
	if (!m->started)
		return;
	pthread_mutex_lock(&m->qlock);
	m->quit = 1;
	pthread_cond_signal(&m->qcond);
	pthread_mutex_unlock(&m->qlock);
	pthread_join(m->th, NULL);
	m->started = 0;
	DBG("pa_threaded_mainloop_stop");
}

void pa_threaded_mainloop_free(pa_threaded_mainloop *m) {
	if (!m)
		return;
	pa_threaded_mainloop_stop(m);
	/* контексты и потоки могут ещё ссылаться на m через события; объект сознательно не освобождается */
	DBG("pa_threaded_mainloop_free");
}

void pa_threaded_mainloop_lock(pa_threaded_mainloop *m) { pthread_mutex_lock(&m->lock); }
void pa_threaded_mainloop_unlock(pa_threaded_mainloop *m) { pthread_mutex_unlock(&m->lock); }
void pa_threaded_mainloop_wait(pa_threaded_mainloop *m) { pthread_cond_wait(&m->wait_cond, &m->lock); }
void pa_threaded_mainloop_signal(pa_threaded_mainloop *m, int wait_for_accept) {
	(void)wait_for_accept;
	pthread_cond_broadcast(&m->wait_cond);
}
pa_mainloop_api *pa_threaded_mainloop_get_api(pa_threaded_mainloop *m) { return &m->api; }

/* ---------- операции ---------- */

struct pa_operation {
	int ref;
	pa_operation_state_t state;
};

static pa_operation *op_new(void) {
	pa_operation *o = calloc(1, sizeof *o);
	o->ref = 2; /* вызывающему + событию, которое её завершит */
	o->state = PA_OPERATION_RUNNING;
	return o;
}
void pa_operation_unref(pa_operation *o) {
	if (o && __atomic_sub_fetch(&o->ref, 1, __ATOMIC_ACQ_REL) == 0)
		free(o);
}
pa_operation_state_t pa_operation_get_state(const pa_operation *o) { return o->state; }
static void op_done(pa_operation *o) {
	o->state = PA_OPERATION_DONE;
	pa_operation_unref(o);
}

/* ---------- громкость ---------- */

static pa_cvolume g_cv;
static float g_gain = 1.0f;
static int g_cv_init;

static float gain_from(pa_volume_t v) {
	if (g_volmode == 2)
		return 1.0f;
	double x = (double)v / (double)PA_VOLUME_NORM;
	if (x < 0)
		x = 0;
	if (x > 1.0)
		x = 1.0; /* усиление выше номинала не даём: защита от клиппинга и от громкого сюрприза */
	if (g_volmode == 1)
		x = x * x * x;
	return (float)x;
}

static void vol_set(const pa_cvolume *cv) {
	g_cv = *cv;
	g_cv_init = 1;
	g_gain = gain_from(pa_cvolume_avg(cv));
	LOG("громкость: avg=%u -> gain=%.3f (режим %s)", (unsigned)pa_cvolume_avg(cv), g_gain,
	    g_volmode == 0 ? "linear" : g_volmode == 1 ? "cubic" : "off");
}

pa_cvolume *pa_cvolume_set(pa_cvolume *a, unsigned channels, pa_volume_t v) {
	if (channels > PA_CHANNELS_MAX)
		channels = PA_CHANNELS_MAX;
	a->channels = (uint8_t)channels;
	for (unsigned i = 0; i < channels; i++)
		a->values[i] = v;
	return a;
}

pa_volume_t pa_cvolume_avg(const pa_cvolume *a) {
	if (!a->channels)
		return PA_VOLUME_MUTED;
	uint64_t s = 0;
	for (unsigned i = 0; i < a->channels; i++)
		s += a->values[i];
	return (pa_volume_t)(s / a->channels);
}

/* ---------- контекст ---------- */

struct pa_stream;
struct pa_context {
	struct pa_threaded_mainloop *ml;
	pa_context_state_t state;
	int err;
	pa_context_notify_cb_t scb;
	void *sud;
	pa_context_subscribe_cb_t subcb;
	void *subud;
	struct pa_stream *stream; /* последний подключённый поток (для get_sink_input_info) */
};

pa_context *pa_context_new(pa_mainloop_api *api, const char *name) {
	pa_context *c = calloc(1, sizeof *c);
	c->ml = (struct pa_threaded_mainloop *)api->userdata;
	c->state = PA_CONTEXT_UNCONNECTED;
	DBG("pa_context_new(%s)", name ? name : "");
	return c;
}

static void ctx_set_state(pa_context *c, pa_context_state_t st) {
	c->state = st;
	DBG("context state=%d", (int)st);
	if (c->scb)
		c->scb(c, c->sud);
}
static void ev_ctx_state(struct ev *e) { ctx_set_state(e->a, (pa_context_state_t)e->i); }

void pa_context_set_state_callback(pa_context *c, pa_context_notify_cb_t cb, void *userdata) {
	c->scb = cb;
	c->sud = userdata;
}

int pa_context_connect(pa_context *c, const char *server, pa_context_flags_t flags, const pa_spawn_api *api) {
	(void)server; (void)flags; (void)api;
	if (c->state != PA_CONTEXT_UNCONNECTED) {
		c->err = PA_ERR_BADSTATE;
		return -1;
	}
	c->state = PA_CONTEXT_CONNECTING;
	ml_post(c->ml, ev_ctx_state, c, NULL, NULL, NULL, PA_CONTEXT_CONNECTING);
	ml_post(c->ml, ev_ctx_state, c, NULL, NULL, NULL, PA_CONTEXT_AUTHORIZING);
	ml_post(c->ml, ev_ctx_state, c, NULL, NULL, NULL, PA_CONTEXT_SETTING_NAME);
	ml_post(c->ml, ev_ctx_state, c, NULL, NULL, NULL, PA_CONTEXT_READY);
	return 0;
}

void pa_context_disconnect(pa_context *c) {
	if (c->state != PA_CONTEXT_UNCONNECTED && c->state != PA_CONTEXT_TERMINATED)
		ctx_set_state(c, PA_CONTEXT_TERMINATED);
}
pa_context_state_t pa_context_get_state(const pa_context *c) { return c->state; }
int pa_context_errno(const pa_context *c) { return c->err; }
void pa_context_unref(pa_context *c) { (void)c; /* объект не освобождается: на него могут ссылаться отложенные события */ }

static void ev_ctx_success(struct ev *e) {
	pa_context *c = e->a;
	pa_context_success_cb_t cb = (pa_context_success_cb_t)e->b;
	if (cb)
		cb(c, 1, e->c);
	op_done(e->d);
}

void pa_context_set_subscribe_callback(pa_context *c, pa_context_subscribe_cb_t cb, void *userdata) {
	c->subcb = cb;
	c->subud = userdata;
}

pa_operation *pa_context_subscribe(pa_context *c, pa_subscription_mask_t m, pa_context_success_cb_t cb, void *userdata) {
	(void)m;
	pa_operation *o = op_new();
	ml_post(c->ml, ev_ctx_success, c, (void *)cb, userdata, o, 0);
	return o;
}

/* ---------- поток ---------- */

struct pa_stream {
	pa_context *ctx;
	pa_stream_state_t state;
	pa_sample_spec ss;
	uint32_t idx;
	size_t in_fs, out_fs; /* размер кадра на входе / выходе (S16) */
	pa_stream_notify_cb_t scb, startedcb, underflowcb;
	void *sud, *startedud, *underflowud;

	pthread_mutex_t rm;
	pthread_cond_t rc;
	uint8_t *ring;
	size_t cap, head, len;
	uint64_t write_bytes, read_bytes;
	int corked, started, quit, reset_out, underflow_sent, running;
	uint64_t gen;
	pthread_t wt;

	int out_fd;
	volatile pid_t child;
	pa_timing_info ti;
};

static pa_stream *g_stream;

static size_t sample_size(pa_sample_format_t f) {
	switch (f) {
	case PA_SAMPLE_U8: case PA_SAMPLE_ALAW: case PA_SAMPLE_ULAW: return 1;
	case PA_SAMPLE_S16LE: case PA_SAMPLE_S16BE: return 2;
	case PA_SAMPLE_S24LE: case PA_SAMPLE_S24BE: return 3;
	case PA_SAMPLE_FLOAT32LE: case PA_SAMPLE_FLOAT32BE: case PA_SAMPLE_S32LE: case PA_SAMPLE_S32BE: return 4;
	default: return 4;
	}
}

size_t pa_usec_to_bytes(pa_usec_t t, const pa_sample_spec *spec) {
	uint64_t frames = (uint64_t)t * spec->rate / 1000000ULL;
	return (size_t)(frames * spec->channels * sample_size(spec->format));
}

pa_stream *pa_stream_new_with_proplist(pa_context *c, const char *name, const pa_sample_spec *ss, const pa_channel_map *map,
				       pa_proplist *p) {
	(void)map; (void)p;
	if (!ss || ss->channels < 1 || ss->channels > 2 || ss->rate < 8000 || ss->rate > 192000 ||
	    (ss->format != PA_SAMPLE_FLOAT32LE && ss->format != PA_SAMPLE_S16LE)) {
		LOG("поток отклонён: формат %d, %u Гц, %u кан. (поддержаны float32le и s16le, 1–2 канала)", ss ? (int)ss->format : -1,
		    ss ? ss->rate : 0, ss ? ss->channels : 0);
		c->err = PA_ERR_NOTSUPPORTED;
		return NULL;
	}
	pa_stream *s = calloc(1, sizeof *s);
	s->ctx = c;
	s->ss = *ss;
	s->state = PA_STREAM_UNCONNECTED;
	s->in_fs = ss->channels * sample_size(ss->format);
	s->out_fs = ss->channels * 2;
	s->idx = 100;
	s->out_fd = -1;
	pthread_mutex_init(&s->rm, NULL);
	pthread_cond_init(&s->rc, NULL);
	LOG("pa_stream_new(%s): формат=%s %u Гц %u кан.", name ? name : "", ss->format == PA_SAMPLE_FLOAT32LE ? "float32le" : "s16le", ss->rate,
	    ss->channels);
	return s;
}

pa_stream *pa_stream_new(pa_context *c, const char *name, const pa_sample_spec *ss, const pa_channel_map *map) {
	return pa_stream_new_with_proplist(c, name, ss, map, NULL);
}

void pa_stream_set_state_callback(pa_stream *s, pa_stream_notify_cb_t cb, void *userdata) { s->scb = cb; s->sud = userdata; }
void pa_stream_set_started_callback(pa_stream *s, pa_stream_notify_cb_t cb, void *userdata) { s->startedcb = cb; s->startedud = userdata; }
void pa_stream_set_underflow_callback(pa_stream *s, pa_stream_notify_cb_t cb, void *userdata) { s->underflowcb = cb; s->underflowud = userdata; }
pa_stream_state_t pa_stream_get_state(const pa_stream *s) { return s->state; }
uint32_t pa_stream_get_index(const pa_stream *s) { return s->idx; }
void pa_stream_unref(pa_stream *s) { (void)s; /* см. pa_context_unref */ }

static void stream_set_state(pa_stream *s, pa_stream_state_t st) {
	s->state = st;
	DBG("stream state=%d", (int)st);
	if (s->scb)
		s->scb(s, s->sud);
}
static void ev_stream_state(struct ev *e) { stream_set_state(e->a, (pa_stream_state_t)e->i); }
static void ev_stream_started(struct ev *e) {
	pa_stream *s = e->a;
	if (s->startedcb && s->state == PA_STREAM_READY)
		s->startedcb(s, s->startedud);
}
static void ev_stream_underflow(struct ev *e) {
	pa_stream *s = e->a;
	if (s->underflowcb && s->state == PA_STREAM_READY)
		s->underflowcb(s, s->underflowud);
}
static void ev_stream_success(struct ev *e) {
	pa_stream *s = e->a;
	pa_stream_success_cb_t cb = (pa_stream_success_cb_t)e->b;
	if (cb)
		cb(s, 1, e->c);
	op_done(e->d);
}

/* ---------- вывод: aplay / FIFO ---------- */

static void out_close(pa_stream *s, int term) {
	pid_t ch = s->child;
	if (s->out_fd >= 0) {
		close(s->out_fd);
		s->out_fd = -1;
	}
	if (ch > 0) {
		if (term)
			kill(ch, SIGTERM);
		for (int i = 0; i < 150; i++) { /* до 1,5 с на штатное завершение */
			int st;
			if (waitpid(ch, &st, WNOHANG) == ch) {
				s->child = 0;
				return;
			}
			usleep(10000);
		}
		kill(ch, SIGKILL);
		waitpid(ch, NULL, 0);
		s->child = 0;
	}
}

static int out_open(pa_stream *s) {
	const char *fifo = getenv("OMD_PCM_FIFO");
	if (fifo && *fifo) {
		int fd = open(fifo, O_WRONLY | O_CLOEXEC);
		if (fd < 0) {
			LOG("не открылся OMD_PCM_FIFO=%s: %s", fifo, strerror(errno));
			return -1;
		}
		s->out_fd = fd;
		LOG("вывод: %s", fifo);
		return 0;
	}
	int p[2];
	if (pipe2(p, O_CLOEXEC) < 0)
		return -1;
	const char *szs = getenv("OMD_PCM_PIPE_BYTES");
	fcntl(p[1], F_SETPIPE_SZ, szs ? atoi(szs) : 16384);

	char rate[16], ch[8];
	snprintf(rate, sizeof rate, "%u", s->ss.rate);
	snprintf(ch, sizeof ch, "%u", s->ss.channels);
	char *argv[40];
	int n = 0;
	char *extra = NULL;
	const char *cmd = getenv("OMD_PCM_CMD");
	if (cmd && *cmd) {
		argv[n++] = "/bin/sh"; argv[n++] = "-c"; argv[n++] = (char *)cmd;
	} else {
		const char *dev = getenv("OMD_PCM_DEV");
		const char *ex = getenv("OMD_PCM_APLAY_ARGS");
		argv[n++] = "aplay"; argv[n++] = "-q";
		argv[n++] = "-D"; argv[n++] = (char *)(dev && *dev ? dev : "default");
		argv[n++] = "-t"; argv[n++] = "raw"; argv[n++] = "-f"; argv[n++] = "S16_LE";
		argv[n++] = "-r"; argv[n++] = rate; argv[n++] = "-c"; argv[n++] = ch;
		extra = strdup(ex ? ex : "--buffer-time=150000");
		for (char *t = strtok(extra, " "); t && n < 38; t = strtok(NULL, " "))
			argv[n++] = t;
	}
	argv[n] = NULL;

	/* окружение: родительское + параметры потока для OMD_PCM_CMD */
	size_t ne = 0;
	while (environ[ne])
		ne++;
	char **envp = calloc(ne + 3, sizeof *envp);
	memcpy(envp, environ, ne * sizeof *envp);
	char e1[40], e2[40];
	snprintf(e1, sizeof e1, "OMD_PCM_RATE=%s", rate);
	snprintf(e2, sizeof e2, "OMD_PCM_CHANNELS=%s", ch);
	envp[ne] = e1;
	envp[ne + 1] = e2;

	posix_spawn_file_actions_t fa;
	posix_spawn_file_actions_init(&fa);
	posix_spawn_file_actions_adddup2(&fa, p[0], 0);
	posix_spawn_file_actions_addopen(&fa, 1, "/dev/null", O_WRONLY, 0);
	pid_t pid;
	int rc = posix_spawnp(&pid, argv[0], &fa, NULL, argv, envp);
	posix_spawn_file_actions_destroy(&fa);
	free(envp);
	free(extra);
	close(p[0]);
	if (rc != 0) {
		LOG("не запустился %s: %s", argv[0], strerror(rc));
		close(p[1]);
		return -1;
	}
	s->out_fd = p[1];
	s->child = pid;
	LOG("вывод: %s pid=%d %u Гц %u кан.", argv[0], (int)pid, s->ss.rate, s->ss.channels);
	return 0;
}

/* ---------- писатель: ring -> S16LE -> вывод ---------- */

static int write_all(int fd, const void *buf, size_t n) {
	const uint8_t *p = buf;
	while (n) {
		ssize_t w = write(fd, p, n);
		if (w < 0) {
			if (errno == EINTR)
				continue;
			return -1;
		}
		p += w;
		n -= (size_t)w;
	}
	return 0;
}

static void convert(const pa_stream *s, const uint8_t *in, size_t frames, int16_t *out, float gain) {
	size_t samples = frames * s->ss.channels;
	if (s->ss.format == PA_SAMPLE_FLOAT32LE) {
		const float *f = (const float *)in; /* ring выровнен, чанки кратны кадру */
		for (size_t i = 0; i < samples; i++) {
			float v = f[i] * gain;
			if (v > 1.0f) v = 1.0f;
			if (v < -1.0f) v = -1.0f;
			out[i] = (int16_t)lrintf(v * 32767.0f);
		}
	} else {
		const int16_t *x = (const int16_t *)in;
		for (size_t i = 0; i < samples; i++) {
			float v = (float)x[i] * gain;
			if (v > 32767.0f) v = 32767.0f;
			if (v < -32768.0f) v = -32768.0f;
			out[i] = (int16_t)lrintf(v);
		}
	}
}

static void *writer_main(void *arg) {
	pa_stream *s = arg;
	sigset_t ss;
	sigemptyset(&ss);
	sigaddset(&ss, SIGPIPE);
	pthread_sigmask(SIG_BLOCK, &ss, NULL);

	size_t chunk = CHUNK_FRAMES * s->in_fs;
	uint8_t *in = aligned_alloc(16, (chunk + 15) & ~(size_t)15);
	int16_t *out = malloc(CHUNK_FRAMES * s->out_fs);
	uint64_t next_spawn = 0;

	for (;;) {
		pthread_mutex_lock(&s->rm);
		for (;;) {
			if (s->quit || s->reset_out)
				break;
			if (!s->corked && s->len > 0)
				break;
			if (!s->corked && s->started && s->len == 0 && !s->underflow_sent && !g_nounderflow) {
				struct timespec t;
				clock_gettime(CLOCK_REALTIME, &t);
				t.tv_nsec += 60 * 1000000L;
				if (t.tv_nsec >= 1000000000L) { t.tv_sec++; t.tv_nsec -= 1000000000L; }
				if (pthread_cond_timedwait(&s->rc, &s->rm, &t) == ETIMEDOUT && s->len == 0 && !s->corked && !s->quit) {
					s->underflow_sent = 1;
					LOG("underflow: данных нет (записано %llu, прочитано %llu байт)", (unsigned long long)s->write_bytes,
					    (unsigned long long)s->read_bytes);
					ml_post(s->ctx->ml, ev_stream_underflow, s, NULL, NULL, NULL, 0);
				}
			} else {
				pthread_cond_wait(&s->rc, &s->rm);
			}
		}
		if (s->quit) {
			pthread_mutex_unlock(&s->rm);
			break;
		}
		if (s->reset_out) {
			s->reset_out = 0;
			pthread_mutex_unlock(&s->rm);
			out_close(s, 1);
			continue;
		}
		size_t n = s->len < chunk ? s->len : chunk;
		n -= n % s->in_fs;
		if (n == 0) {
			pthread_mutex_unlock(&s->rm);
			continue;
		}
		size_t first = s->cap - s->head;
		if (first > n) first = n;
		memcpy(in, s->ring + s->head, first);
		if (n > first)
			memcpy(in + first, s->ring, n - first);
		s->head = (s->head + n) % s->cap;
		s->len -= n;
		s->read_bytes += n;
		uint64_t gen = s->gen;
		int first_data = !s->started;
		if (first_data)
			s->started = 1;
		pthread_mutex_unlock(&s->rm);

		if (first_data) {
			DBG("started");
			ml_post(s->ctx->ml, ev_stream_started, s, NULL, NULL, NULL, 0);
		}
		if (s->out_fd < 0) {
			if (now_ms() < next_spawn) {
				usleep(20000);
				continue;
			}
			if (out_open(s) < 0) {
				next_spawn = now_ms() + 1000;
				continue;
			}
		}
		size_t frames = n / s->in_fs;
		convert(s, in, frames, out, g_gain);
		if (gen != s->gen) /* пока конвертировали, пришёл flush: кусок устарел */
			continue;
		if (write_all(s->out_fd, out, frames * s->out_fs) < 0) {
			int err = errno;
			if (gen != __atomic_load_n(&s->gen, __ATOMIC_ACQUIRE) || __atomic_load_n(&s->reset_out, __ATOMIC_ACQUIRE)) {
				out_close(s, 1); /* обрыв из-за flush: ожидаемо, без штрафа */
				continue;
			}
			LOG("запись в вывод не удалась: %s; перезапуск вывода", strerror(err));
			out_close(s, 1);
			next_spawn = now_ms() + 500;
		}
	}
	out_close(s, 0);
	free(in);
	free(out);
	pthread_mutex_lock(&s->rm);
	free(s->ring);
	s->ring = NULL;
	s->running = 0;
	pthread_mutex_unlock(&s->rm);
	DBG("писатель завершён");
	return NULL;
}

/* ---------- API потока ---------- */

int pa_stream_connect_playback(pa_stream *s, const char *dev, const pa_buffer_attr *attr, pa_stream_flags_t flags,
			       const pa_cvolume *volume, pa_stream *sync_stream) {
	(void)dev; (void)attr; (void)flags; (void)sync_stream;
	if (s->state != PA_STREAM_UNCONNECTED) {
		s->ctx->err = PA_ERR_BADSTATE;
		return -1;
	}
	if (volume)
		vol_set(volume);
	s->cap = (size_t)s->ss.rate * RING_MS / 1000 * s->in_fs;
	s->ring = malloc(s->cap);
	s->state = PA_STREAM_CREATING;
	s->running = 1;
	pthread_attr_t at;
	pthread_attr_init(&at);
	pthread_attr_setdetachstate(&at, PTHREAD_CREATE_DETACHED);
	pthread_attr_setstacksize(&at, 256 * 1024);
	if (pthread_create(&s->wt, &at, writer_main, s) != 0) {
		pthread_attr_destroy(&at);
		free(s->ring);
		s->ring = NULL;
		s->running = 0;
		s->ctx->err = PA_ERR_INTERNAL;
		return -1;
	}
	pthread_attr_destroy(&at);
	s->ctx->stream = s;
	g_stream = s;
	ml_post(s->ctx->ml, ev_stream_state, s, NULL, NULL, NULL, PA_STREAM_READY);
	LOG("pa_stream_connect_playback: буфер %zu байт (%d мс)", s->cap, RING_MS);
	return 0;
}

size_t pa_stream_writable_size(const pa_stream *s) {
	pa_stream *m = (pa_stream *)s;
	if (s->state != PA_STREAM_READY)
		return (size_t)-1;
	pthread_mutex_lock(&m->rm);
	size_t r = s->cap - s->len;
	pthread_mutex_unlock(&m->rm);
	return r - r % s->in_fs;
}

int pa_stream_write(pa_stream *s, const void *data, size_t nbytes, pa_free_cb_t free_cb, int64_t offset, pa_seek_mode_t seek) {
	(void)offset; (void)seek;
	if (s->state != PA_STREAM_READY || !s->ring) {
		if (free_cb)
			free_cb((void *)data);
		s->ctx->err = PA_ERR_BADSTATE;
		return -1;
	}
	pthread_mutex_lock(&s->rm);
	size_t room = s->cap - s->len;
	size_t n = nbytes < room ? nbytes : room;
	n -= n % s->in_fs;
	if (n < nbytes)
		DBG("переполнение буфера: отброшено %zu из %zu байт", nbytes - n, nbytes);
	size_t tail = (s->head + s->len) % s->cap;
	size_t first = s->cap - tail;
	if (first > n) first = n;
	memcpy(s->ring + tail, data, first);
	if (n > first)
		memcpy(s->ring, (const uint8_t *)data + first, n - first);
	s->len += n;
	s->write_bytes += n;
	s->underflow_sent = 0;
	pthread_cond_signal(&s->rc);
	pthread_mutex_unlock(&s->rm);
	if (free_cb)
		free_cb((void *)data);
	return 0;
}

pa_operation *pa_stream_cork(pa_stream *s, int b, pa_stream_success_cb_t cb, void *userdata) {
	pthread_mutex_lock(&s->rm);
	s->corked = b ? 1 : 0;
	if (!b)
		s->started = 0; /* после снятия паузы снова сообщим «воспроизведение началось» */
	pthread_cond_signal(&s->rc);
	pthread_mutex_unlock(&s->rm);
	LOG("cork=%d", b ? 1 : 0);
	pa_operation *o = op_new();
	ml_post(s->ctx->ml, ev_stream_success, s, (void *)cb, userdata, o, 0);
	return o;
}

int pa_stream_is_corked(const pa_stream *s) { return s->corked; }

pa_operation *pa_stream_flush(pa_stream *s, pa_stream_success_cb_t cb, void *userdata) {
	pthread_mutex_lock(&s->rm);
	s->head = 0;
	s->len = 0;
	s->read_bytes = s->write_bytes; /* как в PulseAudio: после flush read_index == write_index */
	s->gen++;
	s->started = 0;
	s->underflow_sent = 0;
	s->reset_out = 1; /* то, что уже ушло в aplay, тоже обрываем */
	pid_t ch = s->child;
	pthread_cond_signal(&s->rc);
	pthread_mutex_unlock(&s->rm);
	if (ch > 0)
		kill(ch, SIGTERM); /* разбудит писателя, если он ждёт в write() */
	LOG("flush");
	pa_operation *o = op_new();
	ml_post(s->ctx->ml, ev_stream_success, s, (void *)cb, userdata, o, 0);
	return o;
}

int pa_stream_disconnect(pa_stream *s) {
	if (s->state != PA_STREAM_READY && s->state != PA_STREAM_CREATING) {
		s->ctx->err = PA_ERR_BADSTATE;
		return -1;
	}
	pthread_mutex_lock(&s->rm);
	s->quit = 1;
	pthread_cond_signal(&s->rc);
	pthread_mutex_unlock(&s->rm);
	if (g_stream == s)
		g_stream = NULL;
	LOG("pa_stream_disconnect");
	stream_set_state(s, PA_STREAM_TERMINATED);
	return 0;
}

static void ti_refresh(pa_stream *s) {
	pthread_mutex_lock(&s->rm);
	uint64_t rd = s->read_bytes, wr = s->write_bytes;
	int playing = s->started && !s->corked;
	pthread_mutex_unlock(&s->rm);
	int unread = 0;
	int fd = s->out_fd;
	if (fd >= 0 && ioctl(fd, FIONREAD, &unread) < 0)
		unread = 0;
	int64_t pending_in = (int64_t)unread * (int64_t)s->in_fs / (int64_t)s->out_fs;
	int64_t ridx = (int64_t)rd - pending_in;
	if (ridx < 0)
		ridx = 0;
	memset(&s->ti, 0, sizeof s->ti);
	gettimeofday(&s->ti.timestamp, NULL);
	s->ti.sink_usec = (pa_usec_t)g_lat_us;
	s->ti.configured_sink_usec = (pa_usec_t)g_lat_us;
	s->ti.playing = playing;
	s->ti.write_index = (int64_t)wr;
	s->ti.read_index = ridx;
}

const pa_timing_info *pa_stream_get_timing_info(pa_stream *s) {
	if (s->state != PA_STREAM_READY)
		return NULL;
	ti_refresh(s);
	return &s->ti;
}

pa_operation *pa_stream_update_timing_info(pa_stream *s, pa_stream_success_cb_t cb, void *userdata) {
	ti_refresh(s);
	pa_operation *o = op_new();
	ml_post(s->ctx->ml, ev_stream_success, s, (void *)cb, userdata, o, 0);
	return o;
}

/* ---------- введение (introspect): громкость и сведения о потоке ---------- */

static void fill_info(pa_sink_input_info *i, uint32_t idx) {
	memset(i, 0, sizeof *i);
	i->index = idx;
	i->name = "Soloist";
	i->driver = "libpulse_shim";
	if (g_stream) {
		i->sample_spec = g_stream->ss;
		i->corked = g_stream->corked;
	} else {
		i->sample_spec.format = PA_SAMPLE_FLOAT32LE;
		i->sample_spec.rate = 44100;
		i->sample_spec.channels = 2;
	}
	i->channel_map.channels = i->sample_spec.channels;
	if (i->sample_spec.channels == 1) {
		i->channel_map.map[0] = PA_CHANNEL_POSITION_MONO;
	} else {
		i->channel_map.map[0] = PA_CHANNEL_POSITION_FRONT_LEFT;
		i->channel_map.map[1] = PA_CHANNEL_POSITION_FRONT_RIGHT;
	}
	if (!g_cv_init)
		pa_cvolume_set(&g_cv, i->sample_spec.channels, PA_VOLUME_NORM);
	i->volume = g_cv;
	i->has_volume = 1;
	i->volume_writable = 1;
}

static void ev_siinfo(struct ev *e) {
	pa_context *c = e->a;
	pa_sink_input_info_cb_t cb = (pa_sink_input_info_cb_t)e->b;
	if (cb) {
		pa_sink_input_info info;
		fill_info(&info, (uint32_t)e->i);
		cb(c, &info, 0, e->c);
		cb(c, NULL, 1, e->c);
	}
	op_done(e->d);
}

pa_operation *pa_context_get_sink_input_info(pa_context *c, uint32_t idx, pa_sink_input_info_cb_t cb, void *userdata) {
	pa_operation *o = op_new();
	ml_post(c->ml, ev_siinfo, c, (void *)cb, userdata, o, (intptr_t)idx);
	return o;
}

pa_operation *pa_context_set_sink_input_volume(pa_context *c, uint32_t idx, const pa_cvolume *volume, pa_context_success_cb_t cb,
					       void *userdata) {
	(void)idx;
	if (volume)
		vol_set(volume);
	pa_operation *o = op_new();
	ml_post(c->ml, ev_ctx_success, c, (void *)cb, userdata, o, 0);
	return o;
}

/* ---------- мелочи ---------- */

pa_proplist *pa_proplist_new(void) { return calloc(1, 16); }
void pa_proplist_free(pa_proplist *p) { free(p); }
int pa_proplist_sets(pa_proplist *p, const char *key, const char *value) {
	(void)p;
	DBG("proplist %s=%s", key ? key : "", value ? value : "");
	return 0;
}

struct timeval *pa_gettimeofday(struct timeval *tv) {
	gettimeofday(tv, NULL);
	return tv;
}

pa_usec_t pa_timeval_diff(const struct timeval *a, const struct timeval *b) {
	int64_t d = ((int64_t)a->tv_sec - (int64_t)b->tv_sec) * 1000000 + ((int64_t)a->tv_usec - (int64_t)b->tv_usec);
	return (pa_usec_t)(d < 0 ? -d : d);
}

const char *pa_strerror(int error) {
	static const char *const m[] = {
		"OK", "Access denied", "Unknown command", "Invalid argument", "Entity exists", "No such entity", "Connection refused",
		"Protocol error", "Timeout", "No authentication key", "Internal error", "Connection terminated", "Entity killed",
		"Invalid server", "Module initialization failed", "Bad state", "No data", "Incompatible protocol version", "Data too large",
		"Operation not supported", "The error code was unknown to the client", "Extension does not exist", "Obsolete functionality",
		"Missing implementation", "The caller forked without calling execve() and tried to reuse the context",
		"An IO error happened", "Device or resource busy"};
	if (error < 0)
		error = -error;
	if (error >= (int)(sizeof m / sizeof m[0]))
		return "Unknown error";
	return m[error];
}

__attribute__((constructor)) static void shim_init(void) {
	const char *v;
	g_dbg = (v = getenv("OMD_SHIM_DEBUG")) && *v && *v != '0';
	g_nounderflow = (v = getenv("OMD_SHIM_NO_UNDERFLOW")) && *v && *v != '0';
	if ((v = getenv("OMD_SHIM_VOLUME")))
		g_volmode = !strcmp(v, "cubic") ? 1 : !strcmp(v, "off") ? 2 : 0;
	if ((v = getenv("OMD_SHIM_LAT_US")) && atol(v) >= 0)
		g_lat_us = atol(v);
	LOG("libpulse_shim " SHIM_VERSION " загружен (громкость: %s)", g_volmode == 0 ? "linear" : g_volmode == 1 ? "cubic" : "off");
}
