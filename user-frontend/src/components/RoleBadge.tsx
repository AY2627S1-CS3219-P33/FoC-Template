import type { Role } from "../api/types";

const LABELS: Record<Role, string> = {
  USER: "User",
  ADMIN: "Administrator",
  SUPER_ADMIN: "Super administrator",
};

// Capabilities summarized from F1.6 for at-a-glance role awareness.
const CAPABILITIES: Record<Role, string> = {
  USER: "Create and deliver errands",
  ADMIN: "Supplier management and order disputes",
  SUPER_ADMIN: "Manage administrator accounts",
};

export function RoleBadge({ role }: { role: Role }) {
  return (
    <span className={`role-badge role-${role.toLowerCase()}`} title={CAPABILITIES[role]}>
      {LABELS[role]}
    </span>
  );
}

export { CAPABILITIES };
