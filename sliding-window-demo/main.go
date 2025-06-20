package main

import (
	crand "crypto/rand"
	"flag"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

const (
	windowSize   = 8  // Sliding window size
	totalPackets = 64 // Total packets to transmit
	chunkSize    = 1024
	fieldSize    = 256
)

// Packet represents a data or coded packet
type Packet struct {
	ID        int
	Data      []byte
	Coeffs    []byte // For coded packets
	IsCoded   bool
	Timestamp time.Time
}

// SlidingWindow represents the sliding window for RLNC
type SlidingWindow struct {
	packets []*Packet
	base    int // Base of the window
	size    int
	mu      sync.Mutex
}

func NewSlidingWindow(size int) *SlidingWindow {
	return &SlidingWindow{
		packets: make([]*Packet, 0, size*2),
		size:    size,
		base:    0,
	}
}

func (sw *SlidingWindow) AddPacket(pkt *Packet) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Add packet to window
	sw.packets = append(sw.packets, pkt)

	// Slide window if we have too many packets
	if len(sw.packets) > sw.size*2 {
		sw.packets = sw.packets[1:]
		sw.base++
	}
}

func (sw *SlidingWindow) GetWindowPackets() []*Packet {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Return packets in current window
	start := len(sw.packets) - sw.size
	if start < 0 {
		start = 0
	}
	return sw.packets[start:]
}

func (sw *SlidingWindow) SlideWindow(ackCount int) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if ackCount > 0 && len(sw.packets) >= ackCount {
		sw.packets = sw.packets[ackCount:]
		sw.base += ackCount
	}
}

// GF represents Galois Field for coding
type GF struct {
	mulTable [][]byte
}

func NewGF() *GF {
	gf := &GF{
		mulTable: make([][]byte, fieldSize),
	}

	for i := 0; i < fieldSize; i++ {
		gf.mulTable[i] = make([]byte, fieldSize)
		for j := 0; j < fieldSize; j++ {
			gf.mulTable[i][j] = byte((i * j) % fieldSize)
		}
	}
	return gf
}

func (gf *GF) Mul(a, b byte) byte {
	return gf.mulTable[a][b]
}

// Sender represents the sliding window RLNC sender
type Sender struct {
	window     *SlidingWindow
	gf         *GF
	codingRate float64 // Ratio of coded packets to data packets
	packetID   int
}

func NewSender(windowSize int, codingRate float64) *Sender {
	return &Sender{
		window:     NewSlidingWindow(windowSize),
		gf:         NewGF(),
		codingRate: codingRate,
		packetID:   0,
	}
}

func (s *Sender) CreateDataPacket() *Packet {
	data := make([]byte, chunkSize)
	crand.Read(data)

	pkt := &Packet{
		ID:        s.packetID,
		Data:      data,
		IsCoded:   false,
		Timestamp: time.Now(),
	}
	s.packetID++

	s.window.AddPacket(pkt)
	return pkt
}

func (s *Sender) CreateCodedPacket() *Packet {
	windowPackets := s.window.GetWindowPackets()
	if len(windowPackets) == 0 {
		return nil
	}

	// Generate random coefficients
	coeffs := make([]byte, len(windowPackets))
	for i := range coeffs {
		coeffs[i] = byte(rand.Intn(fieldSize))
	}

	// Create linear combination
	codedData := make([]byte, chunkSize)
	for i, pkt := range windowPackets {
		if coeffs[i] != 0 {
			for j := range codedData {
				codedData[j] ^= s.gf.Mul(pkt.Data[j], coeffs[i])
			}
		}
	}

	return &Packet{
		ID:        s.packetID,
		Data:      codedData,
		Coeffs:    coeffs,
		IsCoded:   true,
		Timestamp: time.Now(),
	}
}

// Receiver represents the sliding window RLNC receiver
type Receiver struct {
	window  *SlidingWindow
	gf      *GF
	decoded map[int]*Packet
	delays  []time.Duration
	mu      sync.Mutex
}

func NewReceiver(windowSize int) *Receiver {
	return &Receiver{
		window:  NewSlidingWindow(windowSize),
		gf:      NewGF(),
		decoded: make(map[int]*Packet),
		delays:  make([]time.Duration, 0),
	}
}

func (r *Receiver) ReceivePacket(pkt *Packet) bool {
	r.window.AddPacket(pkt)

	if pkt.IsCoded {
		return r.tryDecode()
	} else {
		// Data packet received directly
		r.mu.Lock()
		r.decoded[pkt.ID] = pkt
		r.delays = append(r.delays, time.Since(pkt.Timestamp))
		r.mu.Unlock()
		return true
	}
}

func (r *Receiver) tryDecode() bool {
	windowPackets := r.window.GetWindowPackets()
	if len(windowPackets) < 2 {
		return false
	}

	// Try to decode using received packets
	innovative := r.findInnovativePackets(windowPackets)
	if len(innovative) >= len(r.decoded) {
		// We have enough innovative packets to decode
		for _, pkt := range innovative {
			if !pkt.IsCoded {
				r.mu.Lock()
				r.decoded[pkt.ID] = pkt
				r.delays = append(r.delays, time.Since(pkt.Timestamp))
				r.mu.Unlock()
			}
		}
		return true
	}
	return false
}

func (r *Receiver) findInnovativePackets(packets []*Packet) []*Packet {
	// Simple innovation check - in practice, you'd use matrix rank
	seen := make(map[string]bool)
	innovative := make([]*Packet, 0)

	for _, pkt := range packets {
		key := string(pkt.Data)
		if !seen[key] {
			seen[key] = true
			innovative = append(innovative, pkt)
		}
	}

	return innovative
}

func (r *Receiver) GetStats() (int, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.delays) == 0 {
		return len(r.decoded), 0
	}

	sort.Slice(r.delays, func(i, j int) bool {
		return r.delays[i] < r.delays[j]
	})

	avgDelay := float64(0)
	for _, delay := range r.delays {
		avgDelay += float64(delay.Microseconds())
	}
	avgDelay /= float64(len(r.delays))

	return len(r.decoded), avgDelay
}

// BlockRLNC represents traditional block-based RLNC for comparison
type BlockRLNC struct {
	blockSize int
	gf        *GF
}

func NewBlockRLNC(blockSize int) *BlockRLNC {
	return &BlockRLNC{
		blockSize: blockSize,
		gf:        NewGF(),
	}
}

func (b *BlockRLNC) SimulateBlockTransmission(lossProb float64) (int, float64) {
	// Simulate block-based transmission
	packets := make([]*Packet, b.blockSize)
	for i := 0; i < b.blockSize; i++ {
		data := make([]byte, chunkSize)
		crand.Read(data)
		packets[i] = &Packet{
			ID:        i,
			Data:      data,
			IsCoded:   false,
			Timestamp: time.Now(),
		}
	}

	// Create coded packets
	codedPackets := make([]*Packet, b.blockSize)
	for i := 0; i < b.blockSize; i++ {
		coeffs := make([]byte, b.blockSize)
		for j := range coeffs {
			coeffs[j] = byte(rand.Intn(fieldSize))
		}

		codedData := make([]byte, chunkSize)
		for j, pkt := range packets {
			if coeffs[j] != 0 {
				for k := range codedData {
					codedData[k] ^= b.gf.Mul(pkt.Data[k], coeffs[j])
				}
			}
		}

		codedPackets[i] = &Packet{
			ID:        b.blockSize + i,
			Data:      codedData,
			Coeffs:    coeffs,
			IsCoded:   true,
			Timestamp: time.Now(),
		}
	}

	// Simulate transmission with loss
	received := 0
	delays := make([]time.Duration, 0)

	// Send data packets first
	for _, pkt := range packets {
		if rand.Float64() >= lossProb {
			received++
			delays = append(delays, time.Since(pkt.Timestamp))
		}
	}

	// Send coded packets
	for _, pkt := range codedPackets {
		if rand.Float64() >= lossProb {
			received++
			delays = append(delays, time.Since(pkt.Timestamp))
		}
	}

	// Calculate average delay
	avgDelay := float64(0)
	if len(delays) > 0 {
		for _, delay := range delays {
			avgDelay += float64(delay.Microseconds())
		}
		avgDelay /= float64(len(delays))
	}

	return received, avgDelay
}

func simulateSlidingWindowRLNC(lossProb, codingRate float64) (int, float64) {
	sender := NewSender(windowSize, codingRate)
	receiver := NewReceiver(windowSize)

	// Simulate transmission
	for i := 0; i < totalPackets; i++ {
		// Send data packet
		dataPkt := sender.CreateDataPacket()
		if rand.Float64() >= lossProb {
			receiver.ReceivePacket(dataPkt)
		}

		// Send coded packet based on coding rate
		if rand.Float64() < codingRate {
			codedPkt := sender.CreateCodedPacket()
			if codedPkt != nil && rand.Float64() >= lossProb {
				receiver.ReceivePacket(codedPkt)
			}
		}

		// Simulate ACK and window sliding
		if i%windowSize == 0 && i > 0 {
			sender.window.SlideWindow(windowSize / 2)
		}
	}

	return receiver.GetStats()
}

func main() {
	lossProb := flag.Float64("loss", 0.1, "Packet loss probability")
	codingRate := flag.Float64("rate", 0.5, "Coding rate (ratio of coded packets)")
	blockSize := flag.Int("block", 8, "Block size for block-based RLNC")
	compare := flag.Bool("compare", false, "Compare sliding window vs block-based RLNC")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	if *compare {
		// Compare sliding window vs block-based
		swReceived, swDelay := simulateSlidingWindowRLNC(*lossProb, *codingRate)
		blockReceived, blockDelay := NewBlockRLNC(*blockSize).SimulateBlockTransmission(*lossProb)

		fmt.Printf("Sliding Window vs Block-based RLNC (Loss: %.1f%%, Coding Rate: %.1f)\n", *lossProb*100, *codingRate)
		fmt.Printf("┌─────────────────┬──────────────────┬─────────────────┐\n")
		fmt.Printf("│ Scheme          │ Packets Received │ Avg Delay (μs)  │\n")
		fmt.Printf("├─────────────────┼──────────────────┼─────────────────┤\n")
		fmt.Printf("│ Sliding Window  │ %16d │ %15.1f │\n", swReceived, swDelay)
		fmt.Printf("│ Block-based     │ %16d │ %15.1f │\n", blockReceived, blockDelay)
		fmt.Printf("└─────────────────┴──────────────────┴─────────────────┘\n")

		// Calculate improvements
		delayImprovement := ((blockDelay - swDelay) / blockDelay) * 100
		throughputImprovement := ((float64(swReceived) - float64(blockReceived)) / float64(blockReceived)) * 100

		fmt.Printf("\nKey Results:\n")
		fmt.Printf("• Delay reduction: %.1f%%\n", delayImprovement)
		fmt.Printf("• Throughput improvement: %.1f%%\n", throughputImprovement)
	} else {
		// Single simulation
		received, avgDelay := simulateSlidingWindowRLNC(*lossProb, *codingRate)
		successRate := float64(received) / float64(totalPackets) * 100

		fmt.Printf("Sliding Window RLNC Results\n")
		fmt.Printf("┌─────────────────┬─────────────────┐\n")
		fmt.Printf("│ Metric          │ Value           │\n")
		fmt.Printf("├─────────────────┼─────────────────┤\n")
		fmt.Printf("│ Packets Sent    │ %15d │\n", totalPackets)
		fmt.Printf("│ Packets Received│ %15d │\n", received)
		fmt.Printf("│ Success Rate    │ %14.1f%% │\n", successRate)
		fmt.Printf("│ Avg Delay       │ %14.1f μs │\n", avgDelay)
		fmt.Printf("└─────────────────┴─────────────────┘\n")
	}
}
