package fileops

func (r *runner) coordinatedTransfer(src, dst string, move bool) (outcome, error) {
	return r.transfer(src, dst, move)
}
