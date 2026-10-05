package config

import "testing"

func TestTrafficLoggingFlags(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		args := []string{"--bluecat-host=https://bam.example.com"}
		if enabled {
			args = append(args, "--log-requests", "--log-responses")
		}
		cfg, err := Load(args)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.LogRequests != enabled || cfg.LogResponses != enabled {
			t.Fatalf("unexpected logging flags: requests=%t responses=%t", cfg.LogRequests, cfg.LogResponses)
		}
	}
}
