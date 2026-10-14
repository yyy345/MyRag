package platform

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{"REDIS_URL": "redis://127.0.0.1:56380", "DATABASE_URL": "postgresql://user:private-password@127.0.0.1:55432/migration_test", "JWT_SECRET": strings.Repeat("j", 32), "AI_SERVICE_TOKEN": strings.Repeat("a", 32)}
}
func TestConfigDefaultsAndRedaction(t *testing.T) {
	env := validEnv()
	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTPAddr != "127.0.0.1:8080" || config.AIBaseURL != "http://127.0.0.1:8000" || config.AIParseTimeout != time.Minute {
		t.Fatal("unexpected defaults")
	}
	for _, secret := range []string{config.JWTSecret, config.AIServiceToken, "private-password"} {
		if strings.Contains(fmt.Sprintf("%+v", config), secret) {
			t.Fatal("secret in formatted config")
		}
	}
}
func TestInvalidConfigRejectsWithoutLeakingValues(t *testing.T) {
	for key, values := range map[string][]string{"AI_PARSE_TIMEOUT": {"0s", "-1s", "6m", "bad"}, "DATABASE_URL": {"", "postgresql://%private-password"}, "JWT_SECRET": {"", "short"}, "AI_SERVICE_TOKEN": {"", "short", strings.Repeat("a", 32) + "\n", strings.Repeat("中", 32)}, "GO_HTTP_ADDR": {"localhost:bad", "localhost:0", "localhost:65536"}, "AI_BASE_URL": {"file:///private-password", "https://user:private-password@example.test"}} {
		for _, value := range values {
			t.Run(key+"/"+fmt.Sprint(len(value)), func(t *testing.T) {
				env := validEnv()
				env[key] = value
				_, err := LoadConfig(func(k string) string { return env[k] })
				if err == nil {
					t.Fatal("invalid config accepted")
				}
				if strings.Contains(err.Error(), "private-password") {
					t.Fatal("secret in error")
				}
			})
		}
	}
}
