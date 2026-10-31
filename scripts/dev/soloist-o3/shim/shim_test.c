/*
 * shim_test.c — проверка libpulse_shim.so «как клиент PulseAudio» (так же вызывает функции Soloist).
 * Играет синус float32 44,1 кГц стерео: пауза (cork) -> продолжение -> flush -> громкость через introspect -> дослушивание.
 *
 *   shim_test [секунд=4] [амплитуда=0.20] [частота=440]
 *
 * Выход: 0 — все проверки прошли; 1 — что-то не так (строки FAIL). Звук идёт туда же, куда и у шима (aplay по умолчанию).
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <math.h>
#include <pthread.h>
#include <pulse/pulseaudio.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static const char *NAMES[] = {
	"pa_context_connect", "pa_context_disconnect", "pa_context_errno", "pa_context_get_sink_input_info", "pa_context_get_state",
	"pa_context_new", "pa_context_set_sink_input_volume", "pa_context_set_state_callback", "pa_context_set_subscribe_callback",
	"pa_context_subscribe", "pa_context_unref", "pa_cvolume_avg", "pa_cvolume_set", "pa_gettimeofday", "pa_operation_get_state",
	"pa_operation_unref", "pa_proplist_free", "pa_proplist_new", "pa_proplist_sets", "pa_stream_connect_playback", "pa_stream_cork",
	"pa_stream_disconnect", "pa_stream_flush", "pa_stream_get_index", "pa_stream_get_state", "pa_stream_get_timing_info",
	"pa_stream_is_corked", "pa_stream_new", "pa_stream_new_with_proplist", "pa_stream_set_started_callback",
	"pa_stream_set_state_callback", "pa_stream_set_underflow_callback", "pa_stream_unref", "pa_stream_update_timing_info",
	"pa_stream_writable_size", "pa_stream_write", "pa_strerror", "pa_threaded_mainloop_free", "pa_threaded_mainloop_get_api",
	"pa_threaded_mainloop_lock", "pa_threaded_mainloop_new", "pa_threaded_mainloop_signal", "pa_threaded_mainloop_start",
	"pa_threaded_mainloop_stop", "pa_threaded_mainloop_unlock", "pa_threaded_mainloop_wait", "pa_timeval_diff", "pa_usec_to_bytes",
};

static int fails;
#define CHECK(cond, ...) do { if (cond) printf("ok   " __VA_ARGS__); else { printf("FAIL " __VA_ARGS__); fails++; } printf("\n"); } while (0)

static pa_threaded_mainloop *ml;
static pthread_t ml_tid;
static int cb_thread_ok = 1, started_cnt, underflow_cnt, info_cnt, vol_ok, flush_ok, cork_ok;
static float info_gain = -1;

static void ctx_cb(pa_context *c, void *u) { (void)c; (void)u; pa_threaded_mainloop_signal(ml, 0); }
static void st_cb(pa_stream *s, void *u) { (void)s; (void)u; pa_threaded_mainloop_signal(ml, 0); }
static void started_cb(pa_stream *s, void *u) { (void)s; (void)u; started_cnt++; }
static void underflow_cb(pa_stream *s, void *u) { (void)s; (void)u; underflow_cnt++; }
static void succ_flush(pa_stream *s, int ok, void *u) { (void)s; (void)u; flush_ok = ok; pa_threaded_mainloop_signal(ml, 0); }
static void succ_cork(pa_stream *s, int ok, void *u) { (void)s; (void)u; cork_ok += ok; pa_threaded_mainloop_signal(ml, 0); }
static void succ_vol(pa_context *c, int ok, void *u) { (void)c; (void)u; vol_ok = ok; pa_threaded_mainloop_signal(ml, 0); }
static void info_cb(pa_context *c, const pa_sink_input_info *i, int eol, void *u) {
	(void)c; (void)u;
	if (!eol && i)
		info_cnt++, info_gain = (float)pa_cvolume_avg(&i->volume) / (float)PA_VOLUME_NORM;
	else
		pa_threaded_mainloop_signal(ml, 0);
}
static void wait_op(pa_operation *o) {
	while (pa_operation_get_state(o) == PA_OPERATION_RUNNING)
		pa_threaded_mainloop_wait(ml);
	pa_operation_unref(o);
}

static void feed(pa_stream *s, double *phase, double freq, double amp, double seconds) {
	/* пишем по мере появления места, пока не накопим нужную длительность */
	size_t want = (size_t)(44100 * seconds) * 8, done = 0;
	float buf[4096 * 2];
	while (done < want) {
		size_t room = pa_stream_writable_size(s);
		if (room == (size_t)-1)
			break;
		size_t n = room < sizeof buf ? room : sizeof buf;
		if (n > want - done)
			n = want - done;
		n -= n % 8;
		if (n == 0) {
			pa_threaded_mainloop_unlock(ml);
			usleep(5000);
			pa_threaded_mainloop_lock(ml);
			continue;
		}
		for (size_t i = 0; i < n / 8; i++) {
			float v = (float)(amp * sin(*phase));
			*phase += 2 * M_PI * freq / 44100.0;
			buf[2 * i] = buf[2 * i + 1] = v;
		}
		pa_stream_write(s, buf, n, NULL, 0, PA_SEEK_RELATIVE);
		done += n;
	}
}

int main(int argc, char **argv) {
	double secs = argc > 1 ? atof(argv[1]) : 4, amp = argc > 2 ? atof(argv[2]) : 0.20, freq = argc > 3 ? atof(argv[3]) : 440;

	void *h = dlopen("libpulse.so.0", RTLD_NOW);
	CHECK(h != NULL, "dlopen(libpulse.so.0)%s%s", h ? "" : ": ", h ? "" : dlerror());
	if (!h)
		return 1;
	int missing = 0;
	for (size_t i = 0; i < sizeof NAMES / sizeof *NAMES; i++)
		if (!dlsym(h, NAMES[i])) {
			printf("FAIL нет символа %s\n", NAMES[i]);
			missing++;
		}
	CHECK(missing == 0, "все %zu символов Soloist найдены через dlsym", sizeof NAMES / sizeof *NAMES);

	ml = pa_threaded_mainloop_new();
	ml_tid = pthread_self();
	pa_threaded_mainloop_start(ml);
	pa_threaded_mainloop_lock(ml);
	pa_context *c = pa_context_new(pa_threaded_mainloop_get_api(ml), "shim_test");
	pa_context_set_state_callback(c, ctx_cb, NULL);
	CHECK(pa_context_connect(c, NULL, 0, NULL) == 0, "pa_context_connect");
	while (pa_context_get_state(c) != PA_CONTEXT_READY && pa_context_get_state(c) != PA_CONTEXT_FAILED)
		pa_threaded_mainloop_wait(ml);
	CHECK(pa_context_get_state(c) == PA_CONTEXT_READY, "контекст READY");
	pa_operation *so = pa_context_subscribe(c, PA_SUBSCRIPTION_MASK_SINK_INPUT, NULL, NULL);
	CHECK(so != NULL, "pa_context_subscribe");
	pa_operation_unref(so);

	pa_sample_spec ss = {.format = PA_SAMPLE_FLOAT32LE, .rate = 44100, .channels = 2};
	pa_proplist *pl = pa_proplist_new();
	pa_proplist_sets(pl, PA_PROP_MEDIA_ROLE, "music");
	pa_stream *s = pa_stream_new_with_proplist(c, "tone", &ss, NULL, pl);
	pa_proplist_free(pl);
	CHECK(s != NULL, "pa_stream_new_with_proplist");
	pa_stream_set_state_callback(s, st_cb, NULL);
	pa_stream_set_started_callback(s, started_cb, NULL);
	pa_stream_set_underflow_callback(s, underflow_cb, NULL);
	pa_cvolume cv;
	pa_cvolume_set(&cv, 2, PA_VOLUME_NORM);
	CHECK(pa_stream_connect_playback(s, NULL, NULL, PA_STREAM_ADJUST_LATENCY | PA_STREAM_AUTO_TIMING_UPDATE, &cv, NULL) == 0,
	      "pa_stream_connect_playback");
	while (pa_stream_get_state(s) != PA_STREAM_READY && pa_stream_get_state(s) != PA_STREAM_FAILED)
		pa_threaded_mainloop_wait(ml);
	CHECK(pa_stream_get_state(s) == PA_STREAM_READY, "поток READY, индекс %u", pa_stream_get_index(s));
	CHECK(pa_stream_writable_size(s) > 0, "writable_size = %zu", pa_stream_writable_size(s));

	double ph = 0;
	feed(s, &ph, freq, amp, secs * 0.35);
	pa_threaded_mainloop_unlock(ml);
	usleep(400000);
	pa_threaded_mainloop_lock(ml);
	const pa_timing_info *ti = pa_stream_get_timing_info(s);
	CHECK(ti && ti->write_index > 0 && ti->read_index > 0 && ti->read_index <= ti->write_index, "timing: write=%lld read=%lld sink_usec=%llu playing=%d",
	      ti ? (long long)ti->write_index : -1, ti ? (long long)ti->read_index : -1, ti ? (unsigned long long)ti->sink_usec : 0, ti ? ti->playing : -1);
	CHECK(started_cnt >= 1, "started-callback вызван (%d)", started_cnt);

	/* пауза: индекс чтения перестаёт расти */
	wait_op(pa_stream_cork(s, 1, succ_cork, NULL));
	CHECK(pa_stream_is_corked(s) == 1, "cork(1)");
	pa_threaded_mainloop_unlock(ml);
	usleep(150000);
	pa_threaded_mainloop_lock(ml);
	long long r1 = pa_stream_get_timing_info(s)->read_index;
	feed(s, &ph, freq, amp, 0.2);
	pa_threaded_mainloop_unlock(ml);
	usleep(250000);
	pa_threaded_mainloop_lock(ml);
	long long r2 = pa_stream_get_timing_info(s)->read_index;
	CHECK(r2 == r1, "на паузе read_index не растёт (%lld -> %lld)", r1, r2);
	wait_op(pa_stream_cork(s, 0, succ_cork, NULL));
	CHECK(pa_stream_is_corked(s) == 0 && cork_ok == 2, "cork(0), success-callback (%d)", cork_ok);

	/* громкость: set -> get_sink_input_info */
	pa_cvolume_set(&cv, 2, PA_VOLUME_NORM / 2);
	wait_op(pa_context_set_sink_input_volume(c, pa_stream_get_index(s), &cv, succ_vol, NULL));
	CHECK(vol_ok == 1, "set_sink_input_volume, success");
	wait_op(pa_context_get_sink_input_info(c, pa_stream_get_index(s), info_cb, NULL));
	CHECK(info_cnt == 1 && fabsf(info_gain - 0.5f) < 0.01f, "get_sink_input_info: громкость %.2f (ждали 0.50)", info_gain);
	pa_cvolume_set(&cv, 2, PA_VOLUME_NORM);
	wait_op(pa_context_set_sink_input_volume(c, pa_stream_get_index(s), &cv, succ_vol, NULL));

	feed(s, &ph, freq, amp, secs * 0.25);
	/* flush: буфер пуст, read == write */
	wait_op(pa_stream_flush(s, succ_flush, NULL));
	const pa_timing_info *t2 = pa_stream_get_timing_info(s);
	CHECK(flush_ok == 1 && pa_stream_writable_size(s) > 0, "flush, success; writable_size=%zu", pa_stream_writable_size(s));
	CHECK(t2->read_index <= t2->write_index, "после flush read_index <= write_index (%lld <= %lld)", (long long)t2->read_index, (long long)t2->write_index);

	/* после flush поток продолжает играть */
	feed(s, &ph, freq * 1.5, amp, secs * 0.4);
	pa_threaded_mainloop_unlock(ml);
	usleep(500000);
	pa_threaded_mainloop_lock(ml);
	pa_threaded_mainloop_unlock(ml);
	usleep(1200000); /* дослушать */
	pa_threaded_mainloop_lock(ml);
	CHECK(underflow_cnt >= 1, "underflow-callback после окончания данных (%d)", underflow_cnt);

	pa_stream_disconnect(s);
	CHECK(pa_stream_get_state(s) == PA_STREAM_TERMINATED, "disconnect, TERMINATED");
	pa_stream_unref(s);
	pa_context_disconnect(c);
	pa_context_unref(c);
	pa_threaded_mainloop_unlock(ml);
	pa_threaded_mainloop_stop(ml);
	pa_threaded_mainloop_free(ml);
	CHECK(strcmp(pa_strerror(PA_ERR_ACCESS), "Access denied") == 0, "pa_strerror");
	(void)ml_tid;
	(void)cb_thread_ok;
	usleep(600000); /* дать aplay доиграть и завершиться */
	printf("%s: %d провал(ов)\n", fails ? "ИТОГ ПЛОХО" : "ИТОГ ХОРОШО", fails);
	return fails ? 1 : 0;
}
