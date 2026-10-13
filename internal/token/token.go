// Package token splits a log line into tokens and marks the variable ones by their form
package token

// What a token is as the typed scanners decide
// A built in kind changes templates and therefore template ids
// The set is closed
type Kind uint8

const (
	Const Kind = iota // a word that may be part of a template
	Num               // a number with an optional unit
	Hex               // 0x prefixed or 8 or more mixed hex digits
	IP                // IPv4 with an optional port or IPv6
	UUID              // 8-4-4-4-12 hex
	Time              // RFC 3339, a clock time or an epoch when asked
	Var               // variable by position as the tree sets it
)

var kindNames = [...]string{"const", "num", "hex", "ip", "uuid", "time", "var"}

var kindPlaceholders = [...]string{"", "<num>", "<hex>", "<ip>", "<uuid>", "<time>", "<*>"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// The placeholder of a variable of this kind such as <num>
// Const has none
func (k Kind) Placeholder() string {
	if int(k) < len(kindPlaceholders) {
		return kindPlaceholders[k]
	}
	return "<*>"
}

// A view into one line
// It never holds a copy of the text so a caller keeps the line alive while using it
type Token struct {
	Start int
	End   int
	Kind  Kind
	// Interned id of a constant
	// 0 for a variable
	Sym uint32
}

// The token's text inside line
func (t Token) Bytes(line []byte) []byte { return line[t.Start:t.End] }

// Splits one line into tokens appended to dst
// A tokenizer is safe for concurrent use since it holds configuration only
type Tokenizer interface {
	Tokens(line []byte, dst []Token) []Token
}

// The MaxTokens of a Config that leaves it 0
const DefaultMaxTokens = 128

// ASCII whitespace
// Every tokenizer splits on it and the tree folds it in the text between tokens
func IsSpace(c byte) bool { return spaces[c] }

// A table since the tokenizers and the tree ask for every byte of a line
var spaces = [256]bool{' ': true, '\t': true, '\r': true, '\n': true, '\v': true, '\f': true}

// How a tokenizer splits and scans
type Config struct {
	// Bytes that split tokens besides whitespace under Plain and Punct
	Delimiters string
	// A longer line keeps its first MaxTokens - 1 tokens and one Var token for the rest
	MaxTokens int
	Scan      Scanner
}

func (c Config) maxTokens() int {
	if c.MaxTokens <= 0 {
		return DefaultMaxTokens
	}
	return c.MaxTokens
}

// Caps the token count of a line
// The tokens past the cap fold into one Var token that runs to the end of the last one
func capTokens(dst []Token, from, maxTokens int) []Token {
	if len(dst)-from <= maxTokens {
		return dst
	}
	last := dst[len(dst)-1]
	keep := from + maxTokens - 1
	dst[keep] = Token{Start: dst[keep].Start, End: last.End, Kind: Var}
	return dst[:keep+1]
}
