package entity

// AssemblyExampleCard is one OTHER tech card's assembly tree as raw facts, read for the AI skeleton's
// house style (SuggestAssemblySkeleton, 06-AI-STRUCTURE): the card's header and its JOIN operations
// (a non-empty output unit) in the card's operation order, each with its inputs in order.
type AssemblyExampleCard struct {
	TechCardID   int
	StyleNumber  string
	Name         string
	CategoryName string // "" when the card has no category
	SameCategory bool   // the card shares the asking card's category
	Joins        []AssemblyExampleJoin
}

// AssemblyExampleJoin is one join operation of an example card.
type AssemblyExampleJoin struct {
	OutputUnitKey  string
	OutputUnitName string // "" = the name lives on an earlier producer of the same key, or none
	Inputs         []AssemblyExampleInput
}

// AssemblyExampleInput is one input of a join: a piece (by its name and line key) or a unit (by key).
type AssemblyExampleInput struct {
	UnitKey      string // non-empty for a unit input
	PieceName    string // the piece's name on the card, for a piece input
	PieceLineKey string // the piece's line key, for a piece input
}
