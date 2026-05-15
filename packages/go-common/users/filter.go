package users

import "fmt"

type User struct {
	ID       string
	Email    string
	Phone    string
	Age      int
	Gender   string
	Location string
	Tags     []string
}

type FilterSpec struct {
	MinAge   int
	MaxAge   int
	Gender   string
	Location string
	TagsAny  []string
}

func Filter(users []User, spec FilterSpec) []User {
	out := make([]User, 0, len(users))
	for _, user := range users {
		if matches(user, spec) {
			out = append(out, user)
		}
	}
	return out
}

func PreviewCount(users []User, spec FilterSpec) int {
	return len(Filter(users, spec))
}

func SeedUsers(count int) []User {
	locations := []string{"Moscow", "Kazan", "Saint Petersburg", "Novosibirsk"}
	genders := []string{"female", "male"}
	tagSets := [][]string{{"retail"}, {"b2b"}, {"vip", "retail"}, {"inactive"}}

	out := make([]User, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, User{
			ID:       fmt.Sprintf("user-%05d", i+1),
			Email:    fmt.Sprintf("user%05d@example.com", i+1),
			Phone:    fmt.Sprintf("+7999%07d", i+1),
			Age:      18 + i%45,
			Gender:   genders[i%len(genders)],
			Location: locations[i%len(locations)],
			Tags:     tagSets[i%len(tagSets)],
		})
	}
	return out
}

func matches(user User, spec FilterSpec) bool {
	if spec.MinAge > 0 && user.Age < spec.MinAge {
		return false
	}
	if spec.MaxAge > 0 && user.Age > spec.MaxAge {
		return false
	}
	if spec.Gender != "" && user.Gender != spec.Gender {
		return false
	}
	if spec.Location != "" && user.Location != spec.Location {
		return false
	}
	if len(spec.TagsAny) > 0 && !hasAnyTag(user.Tags, spec.TagsAny) {
		return false
	}
	return true
}

func hasAnyTag(userTags, want []string) bool {
	for _, tag := range userTags {
		for _, expected := range want {
			if tag == expected {
				return true
			}
		}
	}
	return false
}
