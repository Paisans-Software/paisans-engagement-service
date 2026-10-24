package run

import (
	"testing"
	"time"
)

func TestSendTime(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	utc := time.UTC
	cases := []struct {
		name      string
		now, want time.Time
	}{
		{"monday before eight", time.Date(2026, 10, 26, 7, 59, 0, 0, utc), time.Date(2026, 10, 19, 8, 0, 0, 0, utc)},
		{"monday at eight", time.Date(2026, 10, 26, 8, 0, 0, 0, utc), time.Date(2026, 10, 26, 8, 0, 0, 0, utc)},
		{"monday evening", time.Date(2026, 10, 26, 23, 0, 0, 0, utc), time.Date(2026, 10, 26, 8, 0, 0, 0, utc)},
		{"sunday", time.Date(2026, 10, 25, 12, 0, 0, 0, utc), time.Date(2026, 10, 19, 8, 0, 0, 0, utc)},
		// The clocks go back on Sunday 25 October 2026 in London, so the
		// week before the Monday is 169 hours long and the send stays at
		// 08:00 on the wall clock.
		{"after the clocks go back", time.Date(2026, 10, 26, 9, 0, 0, 0, london), time.Date(2026, 10, 26, 8, 0, 0, 0, london)},
		{"sunday the clocks go back", time.Date(2026, 10, 25, 12, 0, 0, 0, london), time.Date(2026, 10, 19, 8, 0, 0, 0, london)},
		// And forward on Sunday 29 March 2026.
		{"after the clocks go forward", time.Date(2026, 3, 30, 8, 0, 0, 0, london), time.Date(2026, 3, 30, 8, 0, 0, 0, london)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SendTime(c.now)
			if !got.Equal(c.want) || got.Location() != c.now.Location() {
				t.Fatalf("SendTime(%v) = %v, want %v", c.now, got, c.want)
			}
			if h, m, _ := got.Clock(); h != 8 || m != 0 || got.Weekday() != time.Monday {
				t.Fatalf("SendTime(%v) = %v is not Monday 08:00 on the wall clock", c.now, got)
			}
		})
	}
}

func TestDue(t *testing.T) {
	monday := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	if week, ok := Due(time.Date(2026, 10, 26, 8, 30, 0, 0, time.UTC)); !ok || !week.Equal(monday) {
		t.Fatalf("Monday 08:30: %v, %v", week, ok)
	}
	if _, ok := Due(time.Date(2026, 10, 27, 7, 59, 0, 0, time.UTC)); !ok {
		t.Fatal("Tuesday 07:59 is still inside the day after the send")
	}
	if _, ok := Due(time.Date(2026, 10, 27, 8, 1, 0, 0, time.UTC)); ok {
		t.Fatal("Tuesday 08:01 is due")
	}
	if _, ok := Due(time.Date(2026, 10, 26, 7, 59, 0, 0, time.UTC)); ok {
		t.Fatal("Monday 07:59 is due: last week's day is long over")
	}
}
