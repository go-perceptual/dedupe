package find

import "sort"

// unionFind is the single-linkage clustering shared by all three perceptual
// passes.
//
// ⛔ It was three copies before, one per pass, and that is how the passes
// drift apart: a fix to the image clustering that never reaches the audio one
// leaves two tools inside one binary. The consequence of single linkage is the
// same for every medium, so the warning belongs in one place too -- A joins
// B's group when A is close to ANY member, not to all of them, so a chain of
// items each close to the next can put the two ends in one group although
// they are far apart. Every caller therefore reports the WIDEST distance or
// the LOWEST score in the group, so a stretched chain is visible.
type unionFind struct {
	parent []int
}

func newUnionFind(n int) *unionFind {
	u := &unionFind{parent: make([]int, n)}
	for i := range u.parent {
		u.parent[i] = i
	}
	return u
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// groups returns the members of every cluster that has more than one member,
// in a deterministic order.
//
// ⛔ Singletons are dropped here rather than by each caller: a group of one is
// not a duplicate, and a pass that reported them would call every file in the
// tree a finding.
func (u *unionFind) groups() [][]int {
	members := map[int][]int{}
	for i := range u.parent {
		r := u.find(i)
		members[r] = append(members[r], i)
	}
	roots := make([]int, 0, len(members))
	for r := range members {
		roots = append(roots, r)
	}
	sort.Ints(roots)

	var out [][]int
	for _, r := range roots {
		if len(members[r]) > 1 {
			out = append(out, members[r])
		}
	}
	return out
}
