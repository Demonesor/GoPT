package main

import (
	"image/color"
	"log"
	"math/rand"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
)

const ChunkSize = 32

type Cell struct {
	Type       uint8
	LastUpdate uint64
}

type Chunk struct {
	Cells    [ChunkSize][ChunkSize]Cell // Поточний стан (тільки читання)
	Next     [ChunkSize][ChunkSize]Cell // Наступний стан (тільки запис)
	IsActive bool
	X, Y     int
}

type World struct {
	Width, Height  int
	ChunkW, ChunkH int
	Chunks         [][]*Chunk
	FrameCount     uint64
}

func NewWorld(w, h int) *World {
	cw, ch := w/ChunkSize, h/ChunkSize
	world := &World{
		Width: w, Height: h,
		ChunkW: cw, ChunkH: ch,
		Chunks: make([][]*Chunk, cw),
	}
	for i := range world.Chunks {
		world.Chunks[i] = make([]*Chunk, ch)
		for j := range world.Chunks[i] {
			world.Chunks[i][j] = &Chunk{X: i, Y: j, IsActive: true}
		}
	}
	return world
}

// --- СИСТЕМА БУФЕРИЗАЦІЇ ---

func (w *World) SwapBuffers() {
	for x := 0; x < w.ChunkW; x++ {
		for y := 0; y < w.ChunkH; y++ {
			c := w.Chunks[x][y]
			c.Cells = c.Next
			// Очищаємо Next (за замовчуванням Type: 0 - повітря)
			c.Next = [ChunkSize][ChunkSize]Cell{}
		}
	}
}

func (w *World) SetCell(gx, gy int, cell Cell) {
	if gx < 0 || gx >= w.Width || gy < 0 || gy >= w.Height {
		return
	}
	cx, cy := gx/ChunkSize, gy/ChunkSize
	// При ручному спавні пишемо в обидва буфери, щоб пісок з'явився миттєво
	w.Chunks[cx][cy].Cells[gx%ChunkSize][gy%ChunkSize] = cell
	w.Chunks[cx][cy].Next[gx%ChunkSize][gy%ChunkSize] = cell
	w.Chunks[cx][cy].IsActive = true
}

func (w *World) SetNextCell(gx, gy int, cell Cell) {
	if gx < 0 || gx >= w.Width || gy < 0 || gy >= w.Height {
		return
	}
	cx, cy := gx/ChunkSize, gy/ChunkSize
	w.Chunks[cx][cy].Next[gx%ChunkSize][gy%ChunkSize] = cell
	w.Chunks[cx][cy].IsActive = true
}

// --- ЛОГІКА ОНОВЛЕННЯ ---

func (w *World) Update() {
	w.FrameCount++

	// 1. Оновлюємо фізику (пишемо з Cells в Next)
	w.updateParity(0)
	w.updateParity(1)

	// 2. Свапаємо буфери
	w.SwapBuffers()
}

func (w *World) updateParity(parity int) {
	var wg sync.WaitGroup
	for x := 0; x < w.ChunkW; x++ {
		for y := 0; y < w.ChunkH; y++ {
			if (x+y)%2 == parity {
				c := w.Chunks[x][y]
				if !c.IsActive {
					continue
				}
				wg.Add(1)
				go func(chunk *Chunk) {
					defer wg.Done()
					w.processChunk(chunk, w.FrameCount)
				}(c)
			}
		}
	}
	wg.Wait()
}

func (w *World) processChunk(c *Chunk, frameNum uint64) {
	for y := ChunkSize - 1; y >= 0; y-- {
		for x := 0; x < ChunkSize; x++ {
			cell := c.Cells[x][y]
			if cell.Type == 0 {
				continue
			}

			gx, gy := c.X*ChunkSize+x, c.Y*ChunkSize+y
			// Перевірка, чи не зайняте місце в наступному кадрі (конфлікт горутин)
			if w.GetNextCellType(gx, gy) != 0 {
				continue
			}

			switch cell.Type {
			case 1: // ПІСОК (Powder)
				if gy+1 < w.Height {
					// 1. Пряме падіння
					if w.GetCellType(gx, gy+1) == 0 && w.GetNextCellType(gx, gy+1) == 0 {
						w.moveCell(gx, gy, gx, gy+1, frameNum)
						continue
					}
					// 2. Сповзання (Angle of Repose)
					dir := 1
					if rand.Intn(2) == 0 {
						dir = -1
					}

					// В TPT пісок має шанс 90% зрушити, що створює тертя
					if rand.Float32() < 0.9 {
						if w.GetCellType(gx+dir, gy+1) == 0 && w.GetNextCellType(gx+dir, gy+1) == 0 {
							w.moveCell(gx, gy, gx+dir, gy+1, frameNum)
							continue
						} else if w.GetCellType(gx-dir, gy+1) == 0 && w.GetNextCellType(gx-dir, gy+1) == 0 {
							w.moveCell(gx, gy, gx-dir, gy+1, frameNum)
							continue
						}
					}
				}

			case 2: // ВОДА (Liquid)
				moved := false
				if gy+1 < w.Height {
					// 1. Вниз
					if w.GetCellType(gx, gy+1) == 0 && w.GetNextCellType(gx, gy+1) == 0 {
						w.moveCell(gx, gy, gx, gy+1, frameNum)
						moved = true
					} else {
						// 2. Діагоналі
						dir := 1
						if rand.Intn(2) == 0 {
							dir = -1
						}
						if w.GetCellType(gx+dir, gy+1) == 0 && w.GetNextCellType(gx+dir, gy+1) == 0 {
							w.moveCell(gx, gy, gx+dir, gy+1, frameNum)
							moved = true
						} else if w.GetCellType(gx-dir, gy+1) == 0 && w.GetNextCellType(gx-dir, gy+1) == 0 {
							w.moveCell(gx, gy, gx-dir, gy+1, frameNum)
							moved = true
						}
					}
				}

				if !moved {
					// 3. Горизонтальний дрифт (Liquid Diffusion)
					// Як у Simulation.cpp, вода намагається проскочити кілька пікселів
					dir := 1
					if rand.Intn(2) == 0 {
						dir = -1
					}

					// Пробуємо "пролетіти" до 5 пікселів вбік
					for dist := 5; dist > 0; dist-- {
						tx := gx + (dir * dist)
						if w.GetCellType(tx, gy) == 0 && w.GetNextCellType(tx, gy) == 0 {
							w.moveCell(gx, gy, tx, gy, frameNum)
							moved = true
							break
						}
					}
				}

				if moved {
					continue
				}
			}

			// Якщо нікуди не ворухнулися — копіюємо себе в майбутнє
			w.SetNextCell(gx, gy, cell)
		}
	}
}
func (w *World) GetNextCellType(gx, gy int) uint8 {
	if gx < 0 || gx >= w.Width || gy < 0 || gy >= w.Height {
		return 255 // Стіна
	}
	cx, cy := gx/ChunkSize, gy/ChunkSize
	return w.Chunks[cx][cy].Next[gx%ChunkSize][gy%ChunkSize].Type
}
func (w *World) moveCell(fromX, fromY, toX, toY int, frameNum uint64) {
	fromCX, fromCY := fromX/ChunkSize, fromY/ChunkSize
	cell := w.Chunks[fromCX][fromCY].Cells[fromX%ChunkSize][fromY%ChunkSize]
	cell.LastUpdate = frameNum

	// Пишемо ТІЛЬКИ в Next
	w.SetNextCell(toX, toY, cell)
}

func (w *World) GetCellType(gx, gy int) uint8 {
	if gx < 0 || gx >= w.Width || gy < 0 || gy >= w.Height {
		return 255 // Кордон світу
	}
	cx, cy := gx/ChunkSize, gy/ChunkSize

	// Читаємо ТІЛЬКИ з поточного стану
	return w.Chunks[cx][cy].Cells[gx%ChunkSize][gy%ChunkSize].Type
}

// --- RENDERING ---

type Game struct {
	world *World
}

func (g *Game) Update() error {
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		mx, my := ebiten.CursorPosition()
		radius := 5
		for i := 0; i < 15; i++ {
			rx := mx + rand.Intn(radius*2) - radius
			ry := my + rand.Intn(radius*2) - radius
			g.world.SetCell(rx, ry, Cell{Type: 1})
		}
	}
	g.world.Update()
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.Black)
	pixels := make([]byte, g.world.Width*g.world.Height*4)
	for cx := 0; cx < g.world.ChunkW; cx++ {
		for cy := 0; cy < g.world.ChunkH; cy++ {
			chunk := g.world.Chunks[cx][cy]
			for x := 0; x < ChunkSize; x++ {
				for y := 0; y < ChunkSize; y++ {
					if chunk.Cells[x][y].Type == 1 {
						idx := ((cy*ChunkSize+y)*g.world.Width + (cx*ChunkSize + x)) * 4
						pixels[idx] = 236
						pixels[idx+1] = 204
						pixels[idx+2] = 120
						pixels[idx+3] = 255
					}
				}
			}
		}
	}
	screen.WritePixels(pixels)
}

func (g *Game) Layout(w, h int) (int, int) { return g.world.Width, g.world.Height }

func main() {
	ww, wh := 320, 256
	game := &Game{world: NewWorld(ww, wh)}
	ebiten.SetWindowSize(ww*2, wh*2)
	ebiten.SetWindowTitle("GoPT Engine v0.1 - Double Buffered")
	ebiten.SetVsyncEnabled(true)
	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}
