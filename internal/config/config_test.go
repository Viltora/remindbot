package config

import (
	"slices"
	"testing"
)

func TestTgIDList(t *testing.T) {
	for raw, want := range map[string][]int64{
		"":                       nil,
		"111111111":              {111111111},
		"111111111,222222222":    {111111111, 222222222},
		" 111111111 , 222222222": {111111111, 222222222},
		"111111111,,111111111":   {111111111},
	} {
		t.Setenv("TEST_IDS", raw)
		got, err := tgIDList("TEST_IDS")
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: got %v, err %v; want %v", raw, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "1;2", "-5", "0"} {
		t.Setenv("TEST_IDS", bad)
		if _, err := tgIDList("TEST_IDS"); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestFlag(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, " true ": true} {
		t.Setenv("TEST_FLAG", raw)
		if got, err := flag("TEST_FLAG"); err != nil || got != want {
			t.Errorf("%q: got %v, err %v; want %v", raw, got, err, want)
		}
	}
	t.Setenv("TEST_FLAG", "да")
	if _, err := flag("TEST_FLAG"); err == nil {
		t.Error("want an error for a non-boolean value")
	}
}
