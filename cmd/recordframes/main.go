// Command recordframes writes camera frames, as the video encoder receives
// them, to a file for codec measurements (the format of
// device.TestRecordCameraFrames: each frame its width and height as uint16
// LE, then width*height*3 bytes of Y, Cb, Cr). The first second is skipped
// while the exposure settles.
//
//	go run ./cmd/recordframes -o call.frames -s 25
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/svanichkin/say/device"
)

func main() {
	out := flag.String("o", "call.frames", "output file")
	secs := flag.Float64("s", 25, "seconds to record")
	flag.Parse()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, err := device.StartCameraStream(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "camera:", err)
		os.Exit(1)
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	start := time.Now().Add(time.Second)
	end := start.Add(time.Duration(*secs * float64(time.Second)))
	fmt.Printf("recording %.0f s to %s (move, turn your head, move the camera)\n", *secs, *out)
	n := 0
	for fr := range frames {
		now := time.Now()
		if now.Before(start) {
			continue
		}
		if now.After(end) {
			break
		}
		var hdr [4]byte
		binary.LittleEndian.PutUint16(hdr[:2], uint16(fr.Width))
		binary.LittleEndian.PutUint16(hdr[2:], uint16(fr.Height))
		if _, err := f.Write(hdr[:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if _, err := f.Write(fr.Data); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		n++
		if n%30 == 0 {
			fmt.Printf("\r%d frames", n)
		}
	}
	fmt.Printf("\r%d frames (%.1f fps) written to %s\n", n, float64(n)/(*secs), *out)
}
