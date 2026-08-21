package pipeline

import "sync"

// FanIn runs fn over every input concurrently and returns the results in the
// same order as the inputs.
func FanIn(inputs []int, workers int, fn func(int) int) []int {
	out := make([]int, len(inputs))

	if len(inputs) == 0 {
		return out
	}

	if workers <= 0 {
		for i := range inputs {
			out[i] = fn(inputs[i])
		}
		return out
	}

	if workers > len(inputs) {
		workers = len(inputs)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out[i] = fn(inputs[i])
			}
		}()
	}

	for i := range inputs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return out
}
