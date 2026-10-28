package registry

import "testing"

func TestTags(t *testing.T) {
	def := Info{Name: "x", Default: true, Ready: true}
	opt := Info{Name: "y", Ready: true}
	planned := Info{Name: "z", Default: true}
	cases := []struct {
		c    Info
		tags []string
		want bool
	}{
		{def, nil, true},
		{def, []string{"omd_no_x"}, false},
		{opt, nil, false},
		{opt, []string{"omd_y"}, true},
		{opt, []string{"omd_no_y"}, false},
		{planned, nil, false},
	}
	for _, c := range cases {
		if got := c.c.InBuild(c.tags); got != c.want {
			t.Errorf("%s с тегами %v: %v, ждали %v", c.c.Name, c.tags, got, c.want)
		}
	}
}

func TestCatalogNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Catalog {
		if seen[c.Name] {
			t.Errorf("повтор %q", c.Name)
		}
		seen[c.Name] = true
		if !c.Ready && c.Phase == 0 {
			t.Errorf("%s: не готов, но фаза не указана", c.Name)
		}
	}
}
