package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Go caches time.Local. Each TZ case needs a fresh process, just like a container.
func TestLogTimezone(t *testing.T) {
	if os.Getenv("SCHEDULER_TZ_TEST") == "1" {
		var output bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&output, nil))
		log.Info("timezone check",
			"summer", time.Date(2026, time.January, 15, 12, 0, 0, 0, time.Local),
			"winter", time.Date(2026, time.July, 15, 12, 0, 0, 0, time.Local))
		_, _ = os.Stdout.Write(output.Bytes())
		os.Exit(0)
	}
	for _, tc := range []struct{ zone, summer, winter string }{
		{"Australia/Sydney", "+11:00", "+10:00"},
		{"UTC", "Z", "Z"},
		{"", "Z", "Z"},
		{"Invalid/Timezone", "Z", "Z"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLogTimezone$")
			cmd.Env = append(os.Environ(), "SCHEDULER_TZ_TEST=1", "TZ="+tc.zone)
			output, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]string
			if err := json.Unmarshal(output, &record); err != nil {
				t.Fatal(err)
			}
			for field, suffix := range map[string]string{"summer": tc.summer, "winter": tc.winter} {
				if !strings.HasSuffix(record[field], suffix) {
					t.Fatalf("%s=%s; want offset %s", field, record[field], suffix)
				}
			}
			stamp, err := time.Parse(time.RFC3339Nano, record["time"])
			if err != nil {
				t.Fatal(err)
			}
			location := time.UTC
			if tc.zone == "Australia/Sydney" {
				location, err = time.LoadLocation(tc.zone)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, got := stamp.Zone()
			_, want := stamp.In(location).Zone()
			if got != want {
				t.Fatalf("log offset %d; want %d", got, want)
			}
		})
	}
}
