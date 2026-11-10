// Shown for nav destinations owned by other services (Home, Errands, Chat). The
// shell reproduces the full wireframe navigation; only User Service screens are
// implemented here.
export function Placeholder({ name }: { name: string }) {
  return (
    <div className="placeholder">
      <h1>{name}</h1>
      <p>This screen belongs to another service and is outside the User Service scope.</p>
    </div>
  );
}
