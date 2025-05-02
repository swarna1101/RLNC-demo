package main

import (
	crand "crypto/rand"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"gonum.org/v1/gonum/mat"
)

const (
	fileSize  = 64 * 1024 // 64 kB
	chunkSize = 1024      // 1 kB per symbol
	k         = fileSize / chunkSize
	fieldSize = 256       // use GF(2⁸)
	numPeers  = 4
	fanout    = 2 // each peer forwards to 2 random peers
)

type Symbol struct {
	Coeff []byte // length k (random coefficients)
	Data  []byte // same length as chunkSize
}

type Msg struct {
	Sym      Symbol
	DataOnly []byte // For plain-gossip mode
}

type Peer struct {
	id       int
	inbox    chan Msg
	outChans []chan Msg // subset of other peers
	received []*Symbol  // innovative symbols collected
	dupCount int
	done     chan struct{} // Signal for shutdown
}

func (p *Peer) run(wg *sync.WaitGroup, plain bool) {
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
						p.forward(msg)
					}
				}
				continue
			}

			if p.isInnovative(&msg.Sym) {
				p.received = append(p.received, &msg.Sym)
				p.forward(msg)
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

func (p *Peer) forward(msg Msg) {
	for _, ch := range p.outChans {
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

func makeCoeff() byte {
	return byte(rand.Intn(fieldSize))
}

func mixSymbol(src []Symbol) Symbol {
	coeff := make([]byte, k)
	data := make([]byte, chunkSize)
	
	// Ensure at least one non-zero coefficient
	hasNonZero := false
	for i := range coeff {
		c := makeCoeff()
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
				data[j] ^= galoisMul(src[i].Data[j], coeff[i])
			}
		}
	}
	
	return Symbol{Coeff: coeff, Data: data}
}

// Simple GF(256) multiplication using lookup table
var gf256Mul [256][256]byte

func init() {
	// Initialize GF(256) multiplication table
	for i := 0; i < 256; i++ {
		for j := 0; j < 256; j++ {
			gf256Mul[i][j] = byte((i * j) % 256)
		}
	}
}

func galoisMul(a, b byte) byte {
	return gf256Mul[a][b]
}

func simulate(plain bool) (avgInnov, avgDup float64) {
	srcSyms := encodeFile()
	
	// Initialize peers with larger buffers
	peers := make([]*Peer, numPeers)
	for i := 0; i < numPeers; i++ {
		peers[i] = &Peer{
			id:       i,
			inbox:    make(chan Msg, 10000), // Increased buffer size
			outChans: make([]chan Msg, 0),
			done:     make(chan struct{}),
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
		go p.run(&wg, plain)
	}

	// Inject data from peer 0
	if plain {
		for _, s := range srcSyms {
			peers[0].forward(Msg{DataOnly: s.Data})
		}
	} else {
		// Send more mixes to ensure enough innovative symbols
		for i := 0; i < k*3; i++ { // Increased from k*2 to k*3
			peers[0].forward(Msg{Sym: mixSymbol(srcSyms)})
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
	}
	avgInnov /= float64(numPeers)
	avgDup /= float64(numPeers)
	return
}

func main() {
	rand.Seed(time.Now().UnixNano())
	
	// Run RLNC simulation
	innov, dup := simulate(false)
	fmt.Printf("RLNC   avg innovative symbols: %.1f  avg dups: %.1f\n", innov, dup)

	// Run plain gossip simulation
	innov, _ = simulate(true)
	fmt.Printf("Plain  avg chunks received   : %.1f  (duplicates not tracked)\n", innov)
} 