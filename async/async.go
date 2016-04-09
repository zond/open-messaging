package async

import (
	"fmt"
	"reflect"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/appengine/delay"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/taskqueue"
)

const (
	contextTaskKey = "async.Task"
)

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	taskType    = reflect.TypeOf((*Task)(nil)).Elem()
)

type Task interface {
	Schedule(context.Context, ...interface{}) error
	ScheduleIn(context.Context, time.Duration, ...interface{}) error
}

type task struct {
	f    *delay.Function
	name string
}

func (t *task) ScheduleIn(c context.Context, pause time.Duration, args ...interface{}) error {
	queueTask, err := t.f.Task(args...)
	if err != nil {
		return err
	}
	queueTask.Delay = pause
	_, err = taskqueue.Add(c, queueTask, t.name)
	return err
}

func (t *task) Schedule(c context.Context, args ...interface{}) error {
	return t.ScheduleIn(c, 0, args...)
}

func FromContext(c context.Context) Task {
	return c.Value(contextTaskKey).(Task)
}

func NewTask(name string, f interface{}) Task {
	val := reflect.ValueOf(f)
	if val.Kind() != reflect.Func {
		panic(fmt.Errorf("%#v is not a func", f))
	}
	if val.Type().NumIn() < 1 {
		panic(fmt.Errorf("%#v doesn't accept at least one arguments", f))
	}
	if val.Type().In(0) != contextType {
		panic(fmt.Errorf("%#v doesn't take a context.Context as first argument", f))
	}
	task := &task{
		name: name,
	}
	wrapper := reflect.MakeFunc(val.Type(), func(args []reflect.Value) []reflect.Value {
		c := context.WithValue(args[0].Interface().(context.Context), contextTaskKey, task)
		argIfs := make([]interface{}, len(args)-1)
		for index, arg := range args[1:] {
			argIfs[index] = arg.Interface()
		}
		log.Debugf(c, "%v(contex.Context, %+v...)", name, argIfs)
		return val.Call(args)
	}).Interface()
	task.f = delay.Func(name, wrapper)
	return task
}
