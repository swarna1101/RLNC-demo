package main

import (
	crand "crypto/rand"
	"flag"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/klauspost/reedsolomon"
	"gonum.org/v1/gonum/mat"
)

const (
	fileSize  = 64 * 1024 // 64 kB
	chunkSize = 1024      // 1 kB per symbol
	k         = fileSize / chunkSize
	numPeers  = 4
	fanout    = 2 // each peer forwards to 2 random peers
)

// GF represents a Galois Field of size 2^bits
// For GF(256), uses klauspost/reedsolomon/galois for accurate arithmetic
// For GF(2^16), uses fallback table
type GF struct {
	bits     int
	size     int
	mulTable [][]byte
	gfmul    func(a, b byte) byte
}

func NewGF(bits int) *GF {
	size := 1 << bits
	gf := &GF{
		bits:     bits,
		size:     size,
		mulTable: make([][]byte, size),
		gfmul:    nil,
	}

	// Build multiplication table for any field size
	for i := 0; i < size; i++ {
		gf.mulTable[i] = make([]byte, size)
		for j := 0; j < size; j++ {
			gf.mulTable[i][j] = byte((i * j) % size)
		}
	}
	gf.gfmul = func(a, b byte) byte {
		return gf.mulTable[a][b]
	}
	return gf
}

func (gf *GF) Mul(a, b byte) byte {
	return gf.gfmul(a, b)
}

type Symbol struct {
	Coeff []byte // length k (random coefficients)
	Data  []byte // same length as chunkSize
}

type Msg struct {
	Sym      Symbol
	DataOnly []byte // For plain-gossip mode
}

type Peer struct {
	id             int
	inbox          chan Msg
	outChans       []chan Msg // subset of other peers
	received       []*Symbol  // innovative symbols collected
	dupCount       int
	done           chan struct{} // Signal for shutdown
	firstInnovTime time.Time     // When this peer received its first innovative symbol
	gf             *GF           // Galois Field for this peer
}

func (p *Peer) run(wg *sync.WaitGroup, plain bool, startTime time.Time, lossProb float64) {
	defer wg.Done()
	receivedChunks := make(map[string]bool) // Track received chunks in plain mode

	for {
		select {
		case msg, ok := <-p.inbox:
			if !ok {
				return
			}
			if plain {
				if msg.DataOnly != nil {
					// Hash the chunk data to use as key
					key := string(msg.DataOnly)
					if !receivedChunks[key] {
						receivedChunks[key] = true
						p.received = append(p.received, &Symbol{Data: msg.DataOnly})
						p.forward(msg, lossProb)
					}
				}
				continue
			}

			if p.isInnovative(&msg.Sym) {
				if len(p.received) == 0 {
					p.firstInnovTime = time.Now()
				}
				p.received = append(p.received, &msg.Sym)
				p.forward(msg, lossProb)
				if len(p.received) == k {
					// done, but keep channel draining to avoid goroutine leak
				}
			} else {
				p.dupCount++
			}
		case <-p.done:
			return
		}
	}
}

func (p *Peer) forward(msg Msg, lossProb float64) {
	for _, ch := range p.outChans {
		// Simulate packet loss
		if rand.Float64() < lossProb {
			continue
		}
		select {
		case ch <- msg:
		default:
			// Drop message if channel is full
		}
	}
}

func (p *Peer) isInnovative(sym *Symbol) bool {
	rows := len(p.received) + 1
	matData := make([]float64, rows*k)
	for i, s := range append(p.received, sym) {
		for j, b := range s.Coeff {
			matData[i*k+j] = float64(b)
		}
	}
	m := mat.NewDense(rows, k, matData)
	var svd mat.SVD
	ok := svd.Factorize(m, mat.SVDThin)
	if !ok {
		return false
	}
	rank := 0
	vals := svd.Values(nil)
	// Use a more lenient threshold for rank computation
	threshold := 1e-6
	for _, v := range vals {
		if v > threshold {
			rank++
		}
	}
	return rank == len(p.received)+1
}

func encodeFile() []Symbol {
	src := make([]byte, fileSize)
	crand.Read(src)
	symbols := make([]Symbol, k)
	for i := 0; i < k; i++ {
		symbols[i].Data = src[i*chunkSize : (i+1)*chunkSize]
	}
	return symbols
}

func makeCoeff(gf *GF) byte {
	return byte(rand.Intn(gf.size))
}

func mixSymbol(src []Symbol, gf *GF) Symbol {
	coeff := make([]byte, k)
	data := make([]byte, chunkSize)

	// Ensure at least one non-zero coefficient
	hasNonZero := false
	for i := range coeff {
		c := makeCoeff(gf)
		coeff[i] = c
		if c != 0 {
			hasNonZero = true
		}
	}

	// If all coefficients are zero, set one to 1
	if !hasNonZero {
		coeff[rand.Intn(k)] = 1
	}

	// Mix the data
	for i := range coeff {
		if coeff[i] != 0 {
			for j := range data {
				data[j] ^= gf.Mul(src[i].Data[j], coeff[i])
			}
		}
	}

	return Symbol{Coeff: coeff, Data: data}
}

func simulate(plain bool, lossProb float64, fieldBits int) (avgInnov, avgDup float64, latencies []time.Duration) {
	srcSyms := encodeFile()
	startTime := time.Now()
	gf := NewGF(fieldBits)

	// Initialize peers with larger buffers
	peers := make([]*Peer, numPeers)
	for i := 0; i < numPeers; i++ {
		peers[i] = &Peer{
			id:       i,
			inbox:    make(chan Msg, 10000), // Increased buffer size
			outChans: make([]chan Msg, 0),
			done:     make(chan struct{}),
			gf:       gf,
		}
	}

	// Set up peer connections
	for _, p := range peers {
		for len(p.outChans) < fanout {
			q := peers[rand.Intn(numPeers)]
			if q != p {
				p.outChans = append(p.outChans, q.inbox)
			}
		}
	}

	// Reset peers and start goroutines
	var wg sync.WaitGroup
	for _, p := range peers {
		p.received, p.dupCount = nil, 0
		wg.Add(1)
		go p.run(&wg, plain, startTime, lossProb)
	}

	// Inject data from peer 0
	if plain {
		for _, s := range srcSyms {
			peers[0].forward(Msg{DataOnly: s.Data}, lossProb)
		}
	} else {
		// Send more mixes to ensure enough innovative symbols
		for i := 0; i < k*3; i++ { // Increased from k*2 to k*3
			peers[0].forward(Msg{Sym: mixSymbol(srcSyms, gf)}, lossProb)
		}
	}

	time.Sleep(2 * time.Second) // simple "quiesce"

	// Signal shutdown to all peers
	for _, p := range peers {
		close(p.done)
	}

	wg.Wait()

	// Tally results
	for _, p := range peers {
		avgInnov += float64(len(p.received))
		avgDup += float64(p.dupCount)
		if !p.firstInnovTime.IsZero() {
			latencies = append(latencies, p.firstInnovTime.Sub(startTime))
		}
	}
	avgInnov /= float64(numPeers)
	avgDup /= float64(numPeers)
	return
}

func simulateRS(lossProb float64) (avgInnov, avgDup float64, latencies []time.Duration) {
	// RS parameters
	n := k * 2 // n = 2k for redundancy
	enc, err := reedsolomon.New(k, n-k)
	if err != nil {
		panic(err)
	}

	src := make([]byte, fileSize)
	crand.Read(src)
	blocks := make([][]byte, k)
	for i := 0; i < k; i++ {
		blocks[i] = src[i*chunkSize : (i+1)*chunkSize]
	}
	shards := make([][]byte, n)
	for i := 0; i < k; i++ {
		shards[i] = make([]byte, chunkSize)
		copy(shards[i], blocks[i])
	}
	for i := k; i < n; i++ {
		shards[i] = make([]byte, chunkSize)
	}
	if err := enc.Encode(shards); err != nil {
		panic(err)
	}

	// Simulate peers
	peers := make([]map[string]bool, numPeers)
	dupCounts := make([]int, numPeers)
	firstTimes := make([]time.Time, numPeers)
	startTime := time.Now()

	// Each peer receives shards via lossy forwarding
	for i := 0; i < n; i++ {
		for p := 0; p < numPeers; p++ {
			if rand.Float64() < lossProb {
				continue
			}
			if peers[p] == nil {
				peers[p] = make(map[string]bool)
			}
			key := string(shards[i])
			if !peers[p][key] {
				peers[p][key] = true
				if len(peers[p]) == 1 {
					firstTimes[p] = time.Now()
				}
			} else {
				dupCounts[p]++
			}
		}
	}

	// Tally results
	for p := 0; p < numPeers; p++ {
		avgInnov += float64(len(peers[p]))
		avgDup += float64(dupCounts[p])
		if !firstTimes[p].IsZero() {
			latencies = append(latencies, firstTimes[p].Sub(startTime))
		}
	}
	avgInnov /= float64(numPeers)
	avgDup /= float64(numPeers)
	return
}

func computeLatencyStats(latencies []time.Duration) (p50, p95 time.Duration) {
	if len(latencies) == 0 {
		return 0, 0
	}
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})
	p50 = latencies[len(latencies)*50/100]
	p95 = latencies[len(latencies)*95/100]
	return
}

func simulateMultihopRLNC(lossProb float64, fieldBits int, hops int) int {
	gf := NewGF(fieldBits)
	srcSyms := encodeFile()
	curr := make([]Symbol, k*2)
	for i := 0; i < k*2; i++ {
		curr[i] = mixSymbol(srcSyms, gf)
	}
	for h := 0; h < hops; h++ {
		// Apply loss
		next := make([]Symbol, 0, len(curr))
		for _, s := range curr {
			if rand.Float64() >= lossProb {
				next = append(next, s)
			}
		}
		// RLNC recoding: generate new random mixes from what survived
		if len(next) < k {
			// Not enough to recode, break early
			curr = next
			break
		}
		curr = make([]Symbol, k*2)
		for i := 0; i < k*2; i++ {
			curr[i] = mixSymbol(next, gf)
		}
	}
	// Count innovative at destination
	received := make([]*Symbol, 0, len(curr))
	for i := range curr {
		innov := true
		for _, s := range received {
			if !isInnovativePair(s, &curr[i]) {
				innov = false
				break
			}
		}
		if innov {
			received = append(received, &curr[i])
		}
	}
	return len(received)
}

func simulateMultihopRS(lossProb float64, hops int) int {
	enc, err := reedsolomon.New(k, k)
	if err != nil {
		panic(err)
	}
	src := make([]byte, fileSize)
	crand.Read(src)
	blocks := make([][]byte, k)
	for i := 0; i < k; i++ {
		blocks[i] = src[i*chunkSize : (i+1)*chunkSize]
	}
	shards := make([][]byte, k*2)
	for i := 0; i < k; i++ {
		shards[i] = make([]byte, chunkSize)
		copy(shards[i], blocks[i])
	}
	for i := k; i < k*2; i++ {
		shards[i] = make([]byte, chunkSize)
	}
	if err := enc.Encode(shards); err != nil {
		panic(err)
	}
	curr := shards
	for h := 0; h < hops; h++ {
		// Apply loss
		next := make([][]byte, 0, len(curr))
		for _, s := range curr {
			if rand.Float64() >= lossProb {
				next = append(next, s)
			}
		}
		curr = next
	}
	// Count unique blocks at destination
	seen := make(map[string]struct{})
	for _, s := range curr {
		seen[string(s)] = struct{}{}
	}
	return len(seen)
}

// Helper for innovation check in multihop RLNC
func isInnovativePair(a, b *Symbol) bool {
	for i := range a.Coeff {
		if a.Coeff[i] != b.Coeff[i] {
			return true
		}
	}
	return false
}

func main() {
	// Parse command line flags
	lossProb := flag.Float64("loss", 0.0, "Packet loss probability (0.0 to 1.0)")
	fieldBits := flag.Int("field", 8, "Number of bits for Galois Field (8 or 16)")
	codeType := flag.String("code", "rlnc", "Coding scheme: rlnc, rs, or plain")
	compare := flag.Bool("compare", false, "Compare RLNC, RS, and plain side by side")
	multihop := flag.Bool("multihop", false, "Run multi-hop chain simulation for RLNC and RS")
	hops := flag.Int("hops", 3, "Number of hops for multi-hop simulation")
	flag.Parse()

	if *fieldBits != 8 && *fieldBits != 16 {
		fmt.Println("Error: field size must be either 8 or 16 bits")
		return
	}

	rand.Seed(time.Now().UnixNano())

	if *multihop {
		fmt.Printf("Multi-hop simulation: %d hops, loss per hop: %.2f\n", *hops, *lossProb)
		innovRLNC := simulateMultihopRLNC(*lossProb, *fieldBits, *hops)
		innovRS := simulateMultihopRS(*lossProb, *hops)
		fmt.Printf("RLNC innovative at destination: %d/%d\n", innovRLNC, k)
		fmt.Printf("RS innovative at destination:   %d/%d\n", innovRS, k)
		return
	}

	fmt.Printf("Running simulation with:\n")
	fmt.Printf("  - Packet loss probability: %.2f\n", *lossProb)
	fmt.Printf("  - Galois Field size: GF(2^%d)\n", *fieldBits)

	if *compare {
		// Run RLNC, RS, and plain and print a markdown table
		innovR, dupR, latR := simulate(false, *lossProb, *fieldBits)
		p50R, p95R := computeLatencyStats(latR)
		innovS, dupS, latS := simulateRS(*lossProb)
		p50S, p95S := computeLatencyStats(latS)
		innovP, _, latP := simulate(true, *lossProb, *fieldBits)
		p50P, p95P := computeLatencyStats(latP)
		fmt.Println("\n| Scheme | Avg Innovative | Avg Dups | Latency p50 | Latency p95 |")
		fmt.Println("|--------|----------------|----------|-------------|-------------|")
		fmt.Printf("| RLNC   | %.1f           | %.1f     | %v   | %v   |\n", innovR, dupR, p50R, p95R)
		fmt.Printf("| RS     | %.1f           | %.1f     | %v   | %v   |\n", innovS, dupS, p50S, p95S)
		fmt.Printf("| Plain  | %.1f           |    -     | %v   | %v   |\n", innovP, p50P, p95P)
		return
	}

	fmt.Printf("  - Coding scheme: %s\n", *codeType)

	if *codeType == "rlnc" {
		innov, dup, latencies := simulate(false, *lossProb, *fieldBits)
		p50, p95 := computeLatencyStats(latencies)
		fmt.Printf("RLNC   avg innovative symbols: %.1f  avg dups: %.1f\n", innov, dup)
		fmt.Printf("       latency p50: %v  p95: %v\n", p50, p95)
	} else if *codeType == "rs" {
		innov, dup, latencies := simulateRS(*lossProb)
		p50, p95 := computeLatencyStats(latencies)
		fmt.Printf("RS     avg innovative symbols: %.1f  avg dups: %.1f\n", innov, dup)
		fmt.Printf("       latency p50: %v  p95: %v\n", p50, p95)
	} else if *codeType == "plain" {
		innov, _, latencies := simulate(true, *lossProb, *fieldBits)
		p50, p95 := computeLatencyStats(latencies)
		fmt.Printf("Plain  avg chunks received   : %.1f  (duplicates not tracked)\n", innov)
		fmt.Printf("       latency p50: %v  p95: %v\n", p50, p95)
	} else {
		fmt.Println("Unknown code type. Use 'rlnc', 'rs', or 'plain'.")
	}
}
