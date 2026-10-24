// Package run sends the weekly digest: when it is due, and to whom.
package run

import "time"

// SendTime is the latest Monday 08:00 in now's location that is not after
// now. It is built from the calendar with time.Date, so it stays at 08:00 on
// the wall clock across a change of daylight saving time.
func SendTime(now time.Time) time.Time {
	y, m, d := now.Date()
	back := (int(now.Weekday()) - int(time.Monday) + 7) % 7
	t := time.Date(y, m, d-back, 8, 0, 0, 0, now.Location())
	if t.After(now) {
		t = time.Date(y, m, d-back-7, 8, 0, 0, 0, now.Location())
	}
	return t
}

// Due reports the week to send and whether its day is still running. A
// service that was down at 08:00 sends late the same day, never days later.
func Due(now time.Time) (week time.Time, ok bool) {
	week = SendTime(now)
	return week, now.Before(week.Add(24 * time.Hour))
}
