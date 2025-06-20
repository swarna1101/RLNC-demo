package main

import (
	"flag"
	"fmt"
	"math/rand"
	"time"
)

// Simple visualization of sliding window RLNC
func visualizeSlidingWindow() {
	fmt.Println("Sliding Window RLNC - Window Movement")
	fmt.Println("=====================================")

	windowSize := 8
	window := make([]int, 0, windowSize*2)
	base := 0

	fmt.Printf("Window size: %d | D=Data packet, C=Coded packet\n\n", windowSize)

	// Simulate packet transmission
	for i := 0; i < 12; i++ {
		// Add data packet
		window = append(window, i)
		fmt.Printf("Step %2d: Add D%d  ", i+1, i)

		// Show current window
		fmt.Printf("Window: [")
		for j := 0; j < windowSize; j++ {
			if j < len(window) {
				if j < len(window)-windowSize/2 {
					fmt.Printf("D%d ", window[j])
				} else {
					fmt.Printf("C%d ", window[j])
				}
			} else {
				fmt.Printf("   ")
			}
		}
		fmt.Printf("] Base: %d\n", base)

		// Slide window occasionally
		if i%4 == 3 && len(window) > windowSize {
			slide := 2
			window = window[slide:]
			base += slide
			fmt.Printf("       → Slide window by %d\n", slide)
		}
	}
}

// Show systematic coding pattern
func visualizeSystematicCoding() {
	fmt.Println("\nSystematic Coding Pattern")
	fmt.Println("=========================")

	codingRate := 0.5
	fmt.Printf("Coding rate: %.1f (1 coded packet per %.0f data packets)\n\n", codingRate, 1/codingRate)

	fmt.Println("Transmission: D1 D1 C1 D2 D2 C2 D3 D3 C3 D4 D4 C4")
	fmt.Println("             ↑  ↑  ↑  ↑  ↑  ↑  ↑  ↑  ↑  ↑  ↑  ↑")
	fmt.Println("             │  │  │  │  │  │  │  │  │  │  │  └─ Coded packet")
	fmt.Println("             │  │  │  │  │  │  │  │  │  │  └───── Data packet")
	fmt.Println("             │  │  │  │  │  │  │  │  │  └──────── Coded packet")
	fmt.Println("             │  │  │  │  │  │  │  │  └─────────── Data packet")
	fmt.Println("             └──┴──┴──┴──┴──┴──┴──┴────────────── Data packets")
}

// Compare with block-based approach
func visualizeBlockComparison() {
	fmt.Println("\nBlock-based vs Sliding Window")
	fmt.Println("=============================")

	fmt.Println("Block-based RLNC:")
	fmt.Println("  [D1][D2][D3][D4] → [C1][C2][C3][C4] → Wait for all → Decode")
	fmt.Println("  ❌ Blocking delay: Must wait for complete block")
	fmt.Println()

	fmt.Println("Sliding Window RLNC:")
	fmt.Println("  D1 → C1 → D2 → C2 → D3 → C3 → ... → Decode when possible")
	fmt.Println("  ✅ No blocking delay: Decode when sufficient symbols received")
	fmt.Println()

	fmt.Println("Key Differences:")
	fmt.Println("  • Block-based: Fixed block size, blocking delay")
	fmt.Println("  • Sliding Window: Dynamic window, no blocking delay")
	fmt.Println("  • Block-based: All-or-nothing decoding")
	fmt.Println("  • Sliding Window: Progressive decoding")
}

func main() {
	showAll := flag.Bool("all", false, "Show all visualizations")
	showWindow := flag.Bool("window", false, "Show sliding window visualization")
	showCoding := flag.Bool("coding", false, "Show systematic coding pattern")
	showCompare := flag.Bool("compare", false, "Show block vs sliding window comparison")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	if *showAll || *showWindow {
		visualizeSlidingWindow()
	}

	if *showAll || *showCoding {
		visualizeSystematicCoding()
	}

	if *showAll || *showCompare {
		visualizeBlockComparison()
	}

	if !*showAll && !*showWindow && !*showCoding && !*showCompare {
		// Default: show all
		visualizeSlidingWindow()
		visualizeSystematicCoding()
		visualizeBlockComparison()
	}
}
