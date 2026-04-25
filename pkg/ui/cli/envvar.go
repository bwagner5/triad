package cli

import (
	"strings"
	"unicode"
)

// FlagToEnvVar derives the canonical env var name for a flag.
// Convention: uppercase(cliName) + "_" + uppercase(flag-with-dashes-to-underscores).
//
//	FlagToEnvVar("lightsailctl", "region")       -> "LIGHTSAILCTL_REGION"
//	FlagToEnvVar("lightsailctl", "wait-timeout") -> "LIGHTSAILCTL_WAIT_TIMEOUT"
//	FlagToEnvVar("my-app",       "no-interactive") -> "MY_APP_NO_INTERACTIVE"
func FlagToEnvVar(cliName, flag string) string {
	return envify(cliName) + "_" + envify(flag)
}

func envify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '-' || r == '.' || r == '/':
			b.WriteByte('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}
