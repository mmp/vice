package sim

var preferredAlphas = []rune{'P', 'K', 'N', 'Y', 'T', 'V', 'F', 'R', 'C', 'D', 'E', 'W', 'A'}
var nonPreferredAlphas = []rune{'M', 'X', 'L', 'J', 'U', 'B', 'G', 'Q', 'S', 'H', 'Z'}

// CIDAllocator manages allocation of unique three-character CIDs.
type CIDAllocator struct {
	List [7]map[string]interface{}
}

func NewCIDAllocator() *CIDAllocator {
	return &CIDAllocator{
		List: generateCIDList(),
	}
}

// Allocate returns the next Available CID.
func (c *CIDAllocator) Allocate() (string, error) {
	var cid string
Top:
	for i, m := range c.List {
		var allValues []string
		for k := range m {
			allValues = append(allValues, k)
		}

		for _, v := range allValues {
			if k, ok := m[v]; ok && k == nil {
				cid = v
				c.List[i][v] = ""
				break Top
			}
		}
	}
	return cid, nil
}

// Release frees a CID so it can be reused.
func (c *CIDAllocator) Release(cid string) {
	for i, m := range c.List {
		if _, ok := m[cid]; ok {
			c.List[i][cid] = nil // Set to nil to mark as available
			return
		}
	}
}

func generateCIDList() [7]map[string]interface{} {
	digits := []rune{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9'}
	var codes [7]map[string]interface{}
	for i := range codes {
		codes[i] = make(map[string]interface{})
	}
	add := func(r1, r2, r3 rune, i int) { codes[i][string([]rune{r1, r2, r3})] = nil }
	// 1. ddd
	for _, a := range digits {
		for _, b := range digits {
			for _, c := range digits {
				add(a, b, c, 0)
			}
		}
	}
	// 2. dda_p
	for _, a := range digits {
		for _, b := range digits {
			for _, l := range preferredAlphas {
				add(a, b, l, 1)
			}
		}
	}
	// 3. da_p a_p
	for _, a := range digits {
		for _, l1 := range preferredAlphas {
			for _, l2 := range preferredAlphas {
				add(a, l1, l2, 2)
			}
		}
	}
	// 4. da_p d
	for _, a := range digits {
		for _, l := range preferredAlphas {
			for _, b := range digits {
				add(a, l, b, 3)
			}
		}
	}
	// 5. dda_n
	for _, a := range digits {
		for _, b := range digits {
			for _, l := range nonPreferredAlphas {
				add(a, b, l, 4)
			}
		}
	}
	// 6. da_n a_n, da_n a_p, da_p a_n
	for _, d := range digits {
		for _, a1 := range nonPreferredAlphas {
			for _, a2 := range nonPreferredAlphas {
				add(d, a1, a2, 5)
			}
			for _, a2 := range preferredAlphas {
				add(d, a1, a2, 5)
			}
		}
		for _, a1 := range preferredAlphas {
			for _, a2 := range nonPreferredAlphas {
				add(d, a1, a2, 5)
			}
		}
	}
	// 7. da_n d
	for _, a := range digits {
		for _, l := range nonPreferredAlphas {
			for _, b := range digits {
				add(a, l, b, 6)
			}
		}
	}
	return codes
}
