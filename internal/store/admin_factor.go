package store

import "context"

func (s *Store) AdminFactor(ctx context.Context) (string, error) {
	var sealed string
	err := s.db.QueryRowContext(ctx, "SELECT sealed FROM admin_factor WHERE id=1").Scan(&sealed)
	return sealed, err
}

func (s *Store) ReplaceAdminFactor(ctx context.Context, previous, sealed string) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE admin_factor SET sealed=? WHERE id=1 AND sealed=?", sealed, previous)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}
