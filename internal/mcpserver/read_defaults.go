package mcpserver

// Explicit invalid values remain invalid; only omitted bounds receive defaults.
// Overflow becomes an invalid end so batch errors remain isolated per item.
func defaultReadRange(start, end *int) (int, int) {
	first, last := 1, 0
	if start != nil {
		first = *start
	}
	if end != nil {
		return first, *end
	}
	if first > 0 && first <= int(^uint(0)>>1)-499 {
		last = first + 499
	}
	return first, last
}
