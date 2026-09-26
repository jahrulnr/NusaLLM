package service

type AutoCompleteService interface {
	Suggestions() []string
}
