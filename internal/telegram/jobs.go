package telegram

import "github.com/riverqueue/river"

// RegisterJobs adds the bot's background jobs to River and returns the
// periodic ones for the client config.
func (t *Bot) RegisterJobs(workers *river.Workers) ([]*river.PeriodicJob, error) {
	var periodic []*river.PeriodicJob
	for _, register := range []func(*river.Workers) ([]*river.PeriodicJob, error){
		t.salesJobs,
		t.planJobs,
		t.taskJobs,
	} {
		jobs, err := register(workers)
		if err != nil {
			return nil, err
		}
		periodic = append(periodic, jobs...)
	}
	return periodic, nil
}
