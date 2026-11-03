package isureal

import (
	"sync"
)

// InMemGraph はユーザー間のフレンド関係およびブロック関係をメモリ上で高速解決するグラフ構造
type InMemGraph struct {
	mu     sync.RWMutex
	edges  map[string]map[string]struct{} // userID -> set of friendIDs
	blocks map[string]map[string]struct{} // blockerID -> set of blockedIDs
}

var GlobalGraph = &InMemGraph{
	edges:  make(map[string]map[string]struct{}),
	blocks: make(map[string]map[string]struct{}),
}

func (g *InMemGraph) AddFriend(u1, u2 string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.edges[u1] == nil {
		g.edges[u1] = make(map[string]struct{})
	}
	if g.edges[u2] == nil {
		g.edges[u2] = make(map[string]struct{})
	}
	g.edges[u1][u2] = struct{}{}
	g.edges[u2][u1] = struct{}{}
}

func (g *InMemGraph) AddBlock(blocker, blocked string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocks[blocker] == nil {
		g.blocks[blocker] = make(map[string]struct{})
	}
	g.blocks[blocker][blocked] = struct{}{}
}

func (g *InMemGraph) IsReady() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.edges) > 0
}

// ComputeFoFAuthors は SQL の WITH mutuals ... fof_authors と完全等価な共通友だち数をメモリ上で算出
func (g *InMemGraph) ComputeFoFAuthors(viewerID string) map[string]int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	myFriends := g.edges[viewerID]
	if len(myFriends) == 0 {
		return nil
	}

	// ブロック関係セット（自分がブロックした相手、自分をブロックした相手）
	isBlocked := make(map[string]bool, 128)
	if bl := g.blocks[viewerID]; bl != nil {
		for uid := range bl {
			isBlocked[uid] = true
		}
	}
	// 逆方向のブロック（相手が自分をブロックしているか）
	for blocker, list := range g.blocks {
		if _, ok := list[viewerID]; ok {
			isBlocked[blocker] = true
		}
	}

	mutualCounts := make(map[string]int64)

	// viewer の友だちを走査
	for fid := range myFriends {
		fFriends := g.edges[fid]
		for candidateID := range fFriends {
			// 自分自身は除外
			if candidateID == viewerID {
				continue
			}
			// 既に直の友だちなら FoF ではないので除外
			if _, ok := myFriends[candidateID]; ok {
				continue
			}
			// ブロック関係にある場合は除外
			if isBlocked[candidateID] {
				continue
			}
			mutualCounts[candidateID]++
		}
	}

	return mutualCounts
}
