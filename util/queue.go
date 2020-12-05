package util

import "sync"

type Job func()

func Worker(jobChan <-chan Job, wg *sync.WaitGroup) {
	for job := range jobChan {
		Process(job, wg)
	}
}

func WorkPool(workerNum int, jobChan chan Job, wg *sync.WaitGroup) {
	for i := 0; i < workerNum; i++ {
		go func(i int) {
			Worker(jobChan, wg)
		}(i)
	}
}

func Enqueue(jobChan chan<- Job, job Job) {
	jobChan <- job
}

func Process(job Job, wg *sync.WaitGroup) {
	defer wg.Done()
	job()
}
