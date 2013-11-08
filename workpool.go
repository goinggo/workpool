// Copyright 2013 Ardan Studios. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
	Package workpool implements a pool of go routines that are dedicated to processing work that is posted into the pool.

		Read the following blog post for more information:blogspot
		http://www.goinggo.net/2013/05/thread-pooling-in-go-programming.html

	New Parameters

	The following is a list of parameters for creating a TraceLog:

		numberOfRoutines: Sets the number of worker routines that are allowed to process work concurrently
		queueCapacity:    Sets the maximum number of pending work objects that can be in queue

	WorkPool Management

	Go routines are used to manage and process all the work. A single Queue routine provides the safe queuing of work.
	The Queue routine keeps track of the amount of work in the queue and reports an error if the queue is full.

	The concurrencyLevel parameter defines the number of work routines to create. These work routines will process work
	subbmitted to the queue. The work routines keep track of the number of active work routines for reporting.

	The PostWork method is used to post work into the ThreadPool. This call will block until the Queue routine reports back
	success or failure that the work is in queue.

	Example Use Of ThreadPool

	The following shows a simple test application

		package main

		import (
		    "github.com/goinggo/workpool"
		    "bufio"
		    "fmt"
		    "os"
		    "runtime"
		    "strconv"
		    "time"
		)

		type MyWork struct {
		    Name      string "The Name of a person"
		    BirthYear int    "The Yea the person was born"
		    WP        *workpool.WorkPool
		}

		func (this *MyWork) DoWork(workRoutine int) {
		    fmt.Printf("%s : %d\n", this.Name, this.BirthYear)
		    fmt.Printf("*******> WR: %d  QW: %d  AR: %d\n", workRoutine, this.WP.QueuedWork(), this.WP.ActiveRoutines())
		    time.Sleep(100 * time.Millisecond)

		    //panic("test")
		}

		func main() {

		    runtime.GOMAXPROCS(runtime.NumCPU())

		    workPool := workpool.New(runtime.NumCPU() * 3, 10)

		    shutdown := false // Just for testing, I Know

		    go func() {

				for i := 0; i < 1000; i++ {

					work := &MyWork{
						Name: "A" + strconv.Itoa(i),
						BirthYear: i,
						WP: workPool,
					}

					err := workPool.PostWork("name_routine", work)

					if err != nil {
						fmt.Printf("ERROR: %s\n", err)
						time.Sleep(100 * time.Millisecond)
					}

					if shutdown == true {
						return
					}
				}
			}()

		    fmt.Println("Hit any key to exit")

		    reader := bufio.NewReader(os.Stdin)
			reader.ReadString('\n')

			shutdown = true

			fmt.Println("Shutting Down\n")
			workPool.Shutdown("name_routine")
		}

	Example Output

	The following shows some sample output

		A336 : 336
		******> QW: 5  AR: 8
		A337 : 337
		*******> QW: 4  AR: 8
		ERROR: Thread Pool At Capacity
		A338 : 338
		*******> QW: 3  AR: 8
		A339 : 339
		*******> QW: 2  AR: 8

		CHANGE FOR ARTICLE
*/
package workpool

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
)

//** NEW TYPES

// poolWork is passed into the queue for work to be performed
type poolWork struct {
	work          PoolWorker // The Work to be performed
	resultChannel chan error // Used to inform the queue operaion is complete
}

// WorkPool implements a work pool with the specified concurrency level and queue capacity
type WorkPool struct {
	shutdownQueueChannel chan string     // Channel used to shut down the queue routine
	shutdownWorkChannel  chan struct{}   // Channel used to shut down the work routines
	shutdownWaitGroup    sync.WaitGroup  // The WaitGroup for shutting down existing routines
	queueChannel         chan poolWork   // Channel used to sync access to the queue
	workChannel          chan PoolWorker // Channel used to process work
	queuedWork           int32           // The number of work items queued
	activeRoutines       int32           // The number of routines active
	queueCapacity        int32           // The max number of items we can store in the queue
}

//** INTERFACES

// PoolWorker must be implemented by the object we will perform work on, now
type PoolWorker interface {
	DoWork(workRoutine int)
}

//** PUBLIC FUNCTIONS

// init is called when the system is inited
func init() {
	log.SetPrefix("TRACE: ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

// New creates a new WorkPool
//	numberOfRoutines: Sets the number of worker routines that are allowed to process work concurrently
//	queueCapacity:    Sets the maximum number of pending work objects that can be in queue
func New(numberOfRoutines int, queueCapacity int32) (workPool *WorkPool) {
	// Create the work pool and initialize it
	workPool = &WorkPool{
		shutdownQueueChannel: make(chan string),
		shutdownWorkChannel:  make(chan struct{}),
		queueChannel:         make(chan poolWork),
		workChannel:          make(chan PoolWorker, queueCapacity),
		queuedWork:           0,
		activeRoutines:       0,
		queueCapacity:        queueCapacity,
	}

	// Launch the work routines to process work
	for workRoutine := 0; workRoutine < numberOfRoutines; workRoutine++ {
		// Add the routine to the wait group
		workPool.shutdownWaitGroup.Add(1)

		// Start a work routine
		go workPool.workRoutine(workRoutine)
	}

	// Start the queue routine to capture and provide work
	go workPool.queueRoutine()

	return workPool
}

//** PUBLIC MEMBER FUNCTIONS

// Shutdown will release resources and shutdown all processing
func (this *WorkPool) Shutdown(goRoutine string) (err error) {
	defer catchPanic(&err, goRoutine, "Shutdown")

	writeStdout(goRoutine, "Shutdown", "Started")
	writeStdout(goRoutine, "Shutdown", "Queue Routine")

	this.shutdownQueueChannel <- "Down"
	<-this.shutdownQueueChannel

	close(this.queueChannel)
	close(this.shutdownQueueChannel)

	writeStdout(goRoutine, "Shutdown", "Shutting Down Work Routines")

	// Close the channel to shut things down
	close(this.shutdownWorkChannel)
	this.shutdownWaitGroup.Wait()

	close(this.workChannel)

	writeStdout(goRoutine, "Shutdown", "Completed")

	return err
}

// PostWork will post work into the WorkPool. This call will block until the Queue routine reports back
// success or failure that the work is in queue.
func (this *WorkPool) PostWork(goRoutine string, work PoolWorker) (err error) {
	defer catchPanic(&err, goRoutine, "PostWork")

	poolWork := poolWork{work, make(chan error)}

	defer close(poolWork.resultChannel)

	this.queueChannel <- poolWork
	err = <-poolWork.resultChannel

	return err
}

// QueuedWork will return the number of work items in queue
func (this *WorkPool) QueuedWork() int32 {
	return atomic.AddInt32(&this.queuedWork, 0)
}

// ActiveRoutines will return the number of routines performing work
func (this *WorkPool) ActiveRoutines() int32 {
	return atomic.AddInt32(&this.activeRoutines, 0)
}

//** PRIVATE FUNCTIONS

// CatchPanic is used to catch any Panic and log exceptions to Stdout. It will also write the stack trace
func catchPanic(err *error, goRoutine string, functionName string) {
	if r := recover(); r != nil {

		// Capture the stack trace
		buf := make([]byte, 10000)
		runtime.Stack(buf, false)

		writeStdoutf(goRoutine, functionName, "PANIC Defered [%v] : Stack Trace : %v", r, string(buf))

		if err != nil {
			*err = fmt.Errorf("%v", r)
		}
	}
}

// writeStdout is used to write a system message directly to stdout
func writeStdout(goRoutine string, functionName string, message string) {
	log.Printf("%s : %s : %s\n", goRoutine, functionName, message)
}

// writeStdoutf is used to write a formatted system message directly stdout
func writeStdoutf(goRoutine string, functionName string, format string, a ...interface{}) {
	writeStdout(goRoutine, functionName, fmt.Sprintf(format, a...))
}

//** PRIVATE MEMBER FUNCTIONS

// workRoutine performs the work required by the work pool
//  workRoutine: The id for the WorkRoutine
func (this *WorkPool) workRoutine(workRoutine int) {
	for {

		select {
		// Shutdown the WorkRoutine
		case <-this.shutdownWorkChannel:
			writeStdout(fmt.Sprintf("WorkRoutine %d", workRoutine), "workRoutine", "Going Down")
			this.shutdownWaitGroup.Done()
			return

		// There is work in the queue
		case poolWorker := <-this.workChannel:
			this.safelyDoWork(workRoutine, poolWorker)
			break
		}
	}
}

// safelyDoWork executes the user DoWork method
//  workRoutine: The internal id of the go routine making the call
//  poolWorker: The work to perform
func (this *WorkPool) safelyDoWork(workRoutine int, poolWorker PoolWorker) {
	defer catchPanic(nil, "WorkRoutine", "SafelyDoWork")
	defer func() {
		atomic.AddInt32(&this.activeRoutines, -1)
	}()

	// Update the counts
	atomic.AddInt32(&this.queuedWork, -1)
	atomic.AddInt32(&this.activeRoutines, 1)

	// Perform the work
	poolWorker.DoWork(workRoutine)
}

// queueRoutine captures and provides work
func (this *WorkPool) queueRoutine() {
	for {
		select {
		// Shutdown the QueueRoutine
		case <-this.shutdownQueueChannel:
			writeStdout("Queue", "queueRoutine", "Going Down")
			this.shutdownQueueChannel <- "Down"
			return

		// Post work to be processed
		case queueItem := <-this.queueChannel:
			// If the queue is at capacity don't add it
			if atomic.AddInt32(&this.queuedWork, 0) == this.queueCapacity {
				queueItem.resultChannel <- fmt.Errorf("Thread Pool At Capacity")
				continue
			}

			// Increment the queued work count
			atomic.AddInt32(&this.queuedWork, 1)

			// Queue the work for the WorkRoutine to process
			this.workChannel <- queueItem.work

			// Tell the caller the work is queued
			queueItem.resultChannel <- nil
			break
		}
	}
}
