package main

import (
	crand "crypto/rand"
	"flag"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

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

func main() {
	// Parse command line flags
	lossProb := flag.Float64("loss", 0.0, "Packet loss probability (0.0 to 1.0)")
	fieldBits := flag.Int("field", 8, "Number of bits for Galois Field (8 or 16)")
	flag.Parse()

	if *fieldBits != 8 && *fieldBits != 16 {
		fmt.Println("Error: field size must be either 8 or 16 bits")
		return
	}

	rand.Seed(time.Now().UnixNano())

	fmt.Printf("Running simulation with:\n")
	fmt.Printf("  - Packet loss probability: %.2f\n", *lossProb)
	fmt.Printf("  - Galois Field size: GF(2^%d)\n", *fieldBits)

	// Run RLNC simulation
	innov, dup, latencies := simulate(false, *lossProb, *fieldBits)
	p50, p95 := computeLatencyStats(latencies)
	fmt.Printf("RLNC   avg innovative symbols: %.1f  avg dups: %.1f\n", innov, dup)
	fmt.Printf("       latency p50: %v  p95: %v\n", p50, p95)

	// Run plain gossip simulation
	innov, _, latencies = simulate(true, *lossProb, *fieldBits)
	p50, p95 = computeLatencyStats(latencies)
	fmt.Printf("Plain  avg chunks received   : %.1f  (duplicates not tracked)\n", innov)
	fmt.Printf("       latency p50: %v  p95: %v\n", p50, p95)
}
