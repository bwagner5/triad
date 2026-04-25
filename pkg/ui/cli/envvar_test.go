package cli

import "testing"

func TestFlagToEnvVar(t *testing.T) {
	cases := []struct {
		cli, flag, want string
	}{
		{"lightsailctl", "region", "LIGHTSAILCTL_REGION"},
		{"lightsailctl", "wait-timeout", "LIGHTSAILCTL_WAIT_TIMEOUT"},
		{"my-app", "no-interactive", "MY_APP_NO_INTERACTIVE"},
		{"triad", "output", "TRIAD_OUTPUT"},
		{"foo.bar", "a/b", "FOO_BAR_A_B"},
	}
	for _, c := range cases {
		if got := FlagToEnvVar(c.cli, c.flag); got != c.want {
			t.Errorf("FlagToEnvVar(%q,%q) = %q; want %q", c.cli, c.flag, got, c.want)
		}
	}
}

func TestEnvOrBool(t *testing.T) {
	env := map[string]string{
		"FOO_SET":  "bar",
		"FOO_FLAG": "true",
	}
	getenv := func(k string) string { return env[k] }

	if v := envOr(getenv, "foo", "set", "default"); v != "bar" {
		t.Errorf("envOr set = %q", v)
	}
	if v := envOr(getenv, "foo", "unset", "default"); v != "default" {
		t.Errorf("envOr unset = %q", v)
	}
	if !envBool(getenv, "foo", "flag", false) {
		t.Errorf("envBool true failed")
	}
	env["FOO_FLAG"] = "nope"
	if envBool(getenv, "foo", "flag", true) {
		t.Errorf("envBool non-truthy should be false, not fallback")
	}
}
