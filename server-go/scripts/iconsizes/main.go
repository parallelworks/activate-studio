// Command iconsizes writes a square PNG at the sizes a Linux desktop looks
// for, for desktop/linux/add_desktop.sh where ImageMagick is not installed:
//
//	go run ./scripts/iconsizes SOURCE.png ICONS_DIR 48 64 128 256 512
//
// writes ICONS_DIR/<n>x<n>/apps/activate-studio.png for each size, resampled
// with Catmull-Rom.
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/image/draw"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: iconsizes SOURCE.png ICONS_DIR SIZE...")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, "iconsizes:", err)
		os.Exit(1)
	}
}

func run(source, dir string, sizes []string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	for _, a := range sizes {
		n, err := strconv.Atoi(a)
		if err != nil || n < 1 || n > 4096 {
			return fmt.Errorf("size %q", a)
		}
		dst := image.NewNRGBA(image.Rect(0, 0, n, n))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		out := filepath.Join(dir, fmt.Sprintf("%dx%d", n, n), "apps", "activate-studio.png")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		if err := png.Encode(w, dst); err != nil {
			w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
	return nil
}
