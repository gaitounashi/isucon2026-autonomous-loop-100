package isureal

import (
	"fmt"
	"testing"
)

func TestInMemGraph_ComputeFoF(t *testing.T) {
	g := &InMemGraph{
		edges:  make(map[string]map[string]struct{}),
		blocks: make(map[string]map[string]struct{}),
	}

	// ユーザー構成:
	// A -- B -- D
	// A -- C -- D
	// A -- C -- E
	// A は D と直接友だちではないが、B と C を介してつながっている (mutual = 2)
	// A は E と直接友だちではないが、C を介してつながっている (mutual = 1)
	g.AddFriend("A", "B")
	g.AddFriend("A", "C")
	g.AddFriend("B", "D")
	g.AddFriend("C", "D")
	g.AddFriend("C", "E")

	fof := g.ComputeFoFAuthors("A")
	if fof["D"] != 2 {
		t.Fatalf("expected mutual count for D to be 2, got %d", fof["D"])
	}
	if fof["E"] != 1 {
		t.Fatalf("expected mutual count for E to be 1, got %d", fof["E"])
	}
	if _, ok := fof["B"]; ok {
		t.Fatalf("direct friend B must not appear in FoF")
	}

	// ブロックの検証: A が D をブロックした場合、D は FoF から消える
	g.AddBlock("A", "D")
	fofAfterBlock := g.ComputeFoFAuthors("A")
	if _, ok := fofAfterBlock["D"]; ok {
		t.Fatalf("blocked user D must not appear in FoF")
	}
}

func BenchmarkInMemGraph_ComputeFoF(b *testing.B) {
	g := &InMemGraph{
		edges:  make(map[string]map[string]struct{}),
		blocks: make(map[string]map[string]struct{}),
	}

	// 100人の友だち、各友だちが50人の友だちを持つリアルなグラフを構築 (計約5,000エッジ)
	for i := 0; i < 100; i++ {
		fid := fmt.Sprintf("friend-%03d", i)
		g.AddFriend("viewer", fid)
		for j := 0; j < 50; j++ {
			fofID := fmt.Sprintf("fof-%04d", (i*20+j)%500)
			g.AddFriend(fid, fofID)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.ComputeFoFAuthors("viewer")
	}
}
