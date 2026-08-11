import type { PoolSession } from '@/types/pool';

export function sessionLabel(sessions: PoolSession[], id: string): string {
    const session = sessions.find((s) => s.id === id);

    if (!session) {
        return id;
    }

    return session.phoneNumber
        ? `${session.label} (${session.phoneNumber})`
        : session.label;
}
