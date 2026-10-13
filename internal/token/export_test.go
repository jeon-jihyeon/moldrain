package token

var (
	IsUUID = isUUID
	IsIP   = isIP
	IsTime = isTime
	IsHex  = isHex
)

// The number of interned texts
func (in *Interner) Len() int {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return len(in.texts) - 1
}
