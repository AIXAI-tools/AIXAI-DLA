package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Tasks paused for a version update come back queued only when the update
// started this process; tasks the user paused stay paused.
func TestLoadJobsResumesOnlyUpdatePaused(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		resumed int
	}{
		{[]string{"app.exe", "--updated-from=4.2.1"}, 1},
		{[]string{"app.exe"}, 0},
	} {
		dir := t.TempDir()
		data := `[{"id":1,"urls":["https://a.example/1"],"state":"paused","autoResume":true},
{"id":2,"urls":["https://b.example/1"],"state":"paused"}]`
		if err := os.WriteFile(filepath.Join(dir, jobsFileName), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		saved := os.Args
		os.Args = tc.args
		a := &app{appShared: &appShared{appDir: dir}}
		got := a.loadJobs()
		os.Args = saved
		if got != tc.resumed {
			t.Fatalf("%v: resumed %d, want %d", tc.args, got, tc.resumed)
		}
		want1 := jobPaused
		if tc.resumed == 1 {
			want1 = jobQueued
		}
		if a.jobs[0].State != want1 || a.jobs[0].AutoResume {
			t.Errorf("%v: job 1 = %s autoResume=%v", tc.args, a.jobs[0].State, a.jobs[0].AutoResume)
		}
		if a.jobs[1].State != jobPaused {
			t.Errorf("%v: user-paused job 2 = %s, want paused", tc.args, a.jobs[1].State)
		}
	}
}
