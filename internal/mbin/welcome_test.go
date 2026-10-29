package mbin

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestNewcomersApprovedLocalSince(t *testing.T) {
	f := newFixture(t)
	since := mid
	f.user(t, 1, row{"created_at": since.Add(time.Hour)})
	f.user(t, 2, row{"created_at": since.Add(2 * time.Hour)})
	f.user(t, 3, row{"created_at": since.Add(time.Hour), "application_status": "Pending"})
	f.user(t, 4, row{"created_at": since.Add(time.Hour), "ap_id": "https://remote.example.org/u/4"})
	f.user(t, 5, row{"created_at": since.Add(time.Hour), "type": "Service"})
	f.user(t, 6, row{"created_at": since.Add(time.Hour), "is_banned": true})
	f.user(t, 7, row{"created_at": since.Add(-time.Minute)})
	ns, err := f.source(t).Newcomers(context.Background(), since, nil, 500)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range ns {
		ids = append(ids, n.ID)
	}
	if !slices.Equal(ids, []int64{1, 2}) {
		t.Fatalf("newcomers = %v, want [1 2]", ids)
	}
	if ns[1].Subject != "sub-2" || ns[1].Username != "u2" {
		t.Fatalf("newcomer = %+v", ns[1])
	}
}

func TestActiveRanksByActivity(t *testing.T) {
	f := newFixture(t)
	since := from
	f.user(t, 1, nil)
	f.user(t, 2, nil)
	f.user(t, 3, nil)
	f.user(t, 4, row{"is_verified": false})
	f.magazine(t, 10, nil)
	f.entry(t, 100, 2, 10, nil)
	f.entry(t, 101, 2, 10, row{"created_at": old})
	f.post(t, 200, 3, 10, nil)
	f.entryComment(t, 300, 3, 100, 10, nil)
	f.exec(t, `INSERT INTO entry_vote VALUES (3, 100, 1, $1)`, mid)
	f.exec(t, `INSERT INTO entry_vote VALUES (1, 100, 0, $1)`, mid)
	f.exec(t, `INSERT INTO post_vote VALUES (1, 200, 1, $1)`, old)
	rs, err := f.source(t).Active(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]int64
	for _, r := range rs {
		got = append(got, [2]int64{r.ID, int64(r.Score)})
	}
	want := [][2]int64{{3, 3}, {2, 1}, {1, 0}}
	if !slices.Equal(got, want) {
		t.Fatalf("active = %v, want %v", got, want)
	}
	if rs[0].Email != "u3@example.org" || rs[0].Subject != "sub-3" {
		t.Fatalf("ranked = %+v", rs[0])
	}
}

func TestCommenters(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 2, nil)
	f.magazine(t, 10, nil)
	f.entry(t, 100, 1, 10, nil)
	f.entry(t, 101, 1, 10, nil)
	f.entryComment(t, 300, 2, 100, 10, nil)
	f.entryComment(t, 301, 1, 101, 10, row{"visibility": "trashed"})
	got, err := f.source(t).Commenters(context.Background(), []int64{100, 101})
	if err != nil {
		t.Fatal(err)
	}
	if !got[100][2] || len(got[100]) != 1 || len(got[101]) != 0 {
		t.Fatalf("commenters = %v", got)
	}
}

func TestSubjectsByEmail(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, row{"email": "Op@Example.org"})
	f.user(t, 2, row{"email": "gone@example.org", "is_deleted": true})
	got, err := f.source(t).SubjectsByEmail(context.Background(), []string{"op@example.ORG", "gone@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["op@example.org"] != "sub-1" {
		t.Fatalf("subjects = %v", got)
	}
}

func TestDigestLeavesOutWelcome(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.user(t, 1, nil)
	f.user(t, 2, nil)
	f.magazine(t, 10, row{"name": WelcomeMagazine, "title": "Welcome"})
	f.magazine(t, 11, row{"name": WelcomeMagazine, "ap_id": "https://remote.example.org/m/welcome"})
	f.entry(t, 100, 1, 10, row{"score": 50, "comment_count": 9})
	f.entry(t, 101, 1, 10, nil)
	f.entry(t, 102, 1, 11, row{"ap_id": "https://remote.example.org/t/1"})
	f.exec(t, `INSERT INTO magazine_subscription VALUES (2, 10), (2, 11)`)
	s := f.source(t)
	hot, err := s.Hot(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if ks := keys(hot); len(ks) != 1 || ks[0] != entryKey(102) {
		t.Fatalf("hot = %v, want only the remote welcome thread", ks)
	}
	fresh, err := s.Unanswered(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 {
		t.Fatalf("unanswered = %v, want none", keys(fresh))
	}
	members, err := s.Members(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.View(ctx, members[1], from, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Following) != 1 || v.Following[0].ID != 11 {
		t.Fatalf("following = %v, want only the remote welcome", v.Following)
	}
}

func TestStatsLeaveOutWelcome(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.magazine(t, 10, nil)
	f.magazine(t, 11, row{"name": "Welcome"}) // Mbin matches magazine names in any case
	f.entry(t, 1, 1, 10, nil)
	f.entry(t, 2, 1, 11, nil)
	f.post(t, 1, 1, 11, nil)
	f.entryComment(t, 1, 1, 2, 11, nil)
	f.postComment(t, 1, 1, 1, 11, nil)
	f.entryComment(t, 2, 1, 1, 10, nil)
	got, err := f.source(t).Stats(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewThreads != 1 || got.Comments != 1 {
		t.Fatalf("stats = %+v, want 1 thread and 1 comment outside /m/welcome", got)
	}
}

func TestActiveCountsLocalContentOnly(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.user(t, 9, row{"ap_id": "https://remote.example.org/u/9"})
	f.magazine(t, 10, nil)
	f.entry(t, 100, 9, 10, row{"ap_id": "https://remote.example.org/t/1"})
	f.post(t, 200, 9, 10, row{"ap_id": "https://remote.example.org/p/1"})
	f.entryComment(t, 300, 1, 100, 10, nil)
	f.postComment(t, 400, 1, 200, 10, nil)
	f.exec(t, `INSERT INTO entry_vote VALUES (1, 100, 1, $1)`, mid)
	f.exec(t, `INSERT INTO post_vote VALUES (1, 200, 1, $1)`, mid)
	f.entry(t, 101, 1, 10, nil)
	f.entryComment(t, 301, 1, 101, 10, nil)
	rs, err := f.source(t).Active(context.Background(), from)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Score != 2 {
		t.Fatalf("active = %+v, want member 1 scoring only the local thread and comment", rs)
	}
}

func TestNewcomersAscendingSkipsWelcomed(t *testing.T) {
	f := newFixture(t)
	for id := 1; id <= 6; id++ {
		f.user(t, id, row{"created_at": mid.Add(time.Duration(id) * time.Minute)})
	}
	ns, err := f.source(t).Newcomers(context.Background(), mid, []int64{1, 3}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, n := range ns {
		ids = append(ids, n.ID)
	}
	if !slices.Equal(ids, []int64{2, 4}) {
		t.Fatalf("newcomers = %v, want [2 4]", ids)
	}
}

func TestWelcomeThread(t *testing.T) {
	f := newFixture(t)
	f.user(t, 1, nil)
	f.magazine(t, 10, nil)
	f.magazine(t, 11, row{"name": "welcome"})
	f.entry(t, 100, 1, 10, row{"title": "Welcome @a"})
	f.entry(t, 101, 1, 11, row{"title": "Welcome @a", "created_at": old})
	f.entry(t, 102, 1, 11, row{"title": "Welcome @a", "slug": "welcome-a"})
	s := f.source(t)
	id, slug, found, err := s.WelcomeThread(context.Background(), "Welcome @a", from)
	if err != nil || !found || id != 102 || slug != "welcome-a" {
		t.Fatalf("WelcomeThread = %d %q %v %v", id, slug, found, err)
	}
	if _, _, found, _ := s.WelcomeThread(context.Background(), "Welcome @b", from); found {
		t.Fatal("found a thread that names someone else")
	}
}
