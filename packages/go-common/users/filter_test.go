package users

import "testing"

func TestFilterUsersBySegment(t *testing.T) {
	fixtures := []User{
		{ID: "1", Age: 25, Gender: "female", Location: "Moscow", Tags: []string{"vip", "retail"}},
		{ID: "2", Age: 37, Gender: "male", Location: "Kazan", Tags: []string{"b2b"}},
		{ID: "3", Age: 29, Gender: "female", Location: "Moscow", Tags: []string{"b2b", "retail"}},
	}

	got := Filter(fixtures, FilterSpec{MinAge: 20, MaxAge: 30, Gender: "female", Location: "Moscow", TagsAny: []string{"vip"}})
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("unexpected filtered users: %#v", got)
	}
}

func TestPreviewCount(t *testing.T) {
	fixtures := SeedUsers(50000)
	count := PreviewCount(fixtures, FilterSpec{Location: "Moscow", TagsAny: []string{"retail"}})
	if count == 0 {
		t.Fatal("expected non-empty preview count")
	}
}
