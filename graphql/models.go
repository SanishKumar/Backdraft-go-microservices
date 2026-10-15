package graphql

// Account's orders are resolved lazily by accountResolver (see gqlgen.yml).
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
