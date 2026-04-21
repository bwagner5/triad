package duration

import (
	"testing"
	"time"
)

func TestShort(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "0s"},
		{10 * time.Second, "10s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{2 * time.Minute, "2m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := Short(c.d); got != c.want {
			t.Errorf("Short(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
