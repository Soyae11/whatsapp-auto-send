import { Head, router } from '@inertiajs/react';
import { useMemo, useState } from 'react';
import {
    addMember,
    destroy,
    promote,
    reinstate,
    removeMember,
    store,
} from '@/actions/App/Http/Controllers/PoolsController';
import { SessionPickerDialog } from '@/components/session-picker-dialog';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { pools as poolsRoute } from '@/routes';
import type { PoolMember, PoolSession } from '@/types/pool';
import type { SenderOption } from '@/types/send';

function sessionLabel(sessions: PoolSession[], id: string): string {
    const session = sessions.find((s) => s.id === id);

    if (!session) {
        return id;
    }

    return session.phoneNumber
        ? `${session.label} (${session.phoneNumber})`
        : session.label;
}

function MemberBadge({ member }: { member: PoolMember }) {
    if (member.is_main) {
        return <Badge>main</Badge>;
    }

    if (member.disqualified) {
        return <Badge variant="destructive">disqualified</Badge>;
    }

    return <Badge variant="secondary">backup</Badge>;
}

// A session is unavailable once it's a member of any pool — a session can't serve two pools'
// rotation logic at once. A sender's own un-pooled default session is deliberately *not*
// excluded here: sharing an active sender's session as another sender's backup is normal in a
// small deployment with more senders than physical sessions.
function sessionsInAnyPool(pools: Record<string, PoolMember[]>): Set<string> {
    return new Set(
        Object.values(pools)
            .flat()
            .map((m) => m.session_id),
    );
}

export default function Pools({
    senders,
    pools,
    sessions,
}: {
    senders: SenderOption[];
    pools: Record<string, PoolMember[]>;
    sessions: PoolSession[];
}) {
    const [addingBackupFor, setAddingBackupFor] = useState<string | null>(null);
    const [createSender, setCreateSender] = useState('');
    const [createSession, setCreateSession] = useState('');

    const poolableSenders = senders.filter(
        (s) => (pools[s.name] ?? []).length === 0,
    );
    const pooledSenders = senders.filter(
        (s) => (pools[s.name] ?? []).length > 0,
    );

    const createSessionOptions = useMemo(() => {
        if (!createSender) {
            return [];
        }

        const unavailable = sessionsInAnyPool(pools);

        return sessions.filter((s) => !unavailable.has(s.id));
    }, [createSender, pools, sessions]);

    function createPool() {
        if (!createSender || !createSession) {
            return;
        }

        router.post(store.url(), {
            sender: createSender,
            sessions: [createSession],
        });
        setCreateSender('');
        setCreateSession('');
    }

    function addBackup(sender: string, sessionId: string) {
        router.post(addMember.url({ sender }), { session_id: sessionId });
        setAddingBackupFor(null);
    }

    function doPromote(sender: string, sessionId: string) {
        router.post(promote.url({ sender }), { session_id: sessionId });
    }

    function doRemove(sender: string, sessionId: string) {
        router.delete(removeMember.url({ sender, sessionId }));
    }

    function doReinstate(sender: string, sessionId: string) {
        router.post(reinstate.url({ sender, sessionId }));
    }

    function doDeletePool(sender: string) {
        router.delete(destroy.url({ sender }));
    }

    return (
        <>
            <Head title="Pools" />
            <div className="flex flex-1 flex-col gap-4 p-4">
                {poolableSenders.length > 0 && (
                    <Card>
                        <CardHeader>
                            <CardTitle>Create a pool</CardTitle>
                        </CardHeader>
                        <CardContent>
                            <div className="flex flex-wrap items-end gap-4">
                                <div className="flex flex-col gap-2">
                                    <span className="text-sm font-medium">
                                        Sender
                                    </span>
                                    <Select
                                        value={createSender}
                                        onValueChange={(value) => {
                                            setCreateSender(value);
                                            setCreateSession('');
                                        }}
                                    >
                                        <SelectTrigger className="w-56">
                                            <SelectValue placeholder="Choose a sender" />
                                        </SelectTrigger>
                                        <SelectContent>
                                            {poolableSenders.map((sender) => (
                                                <SelectItem
                                                    key={sender.name}
                                                    value={sender.name}
                                                >
                                                    {sender.name}
                                                </SelectItem>
                                            ))}
                                        </SelectContent>
                                    </Select>
                                </div>

                                <div className="flex flex-col gap-2">
                                    <span className="text-sm font-medium">
                                        Session
                                    </span>
                                    <Select
                                        value={createSession}
                                        onValueChange={setCreateSession}
                                        disabled={!createSender}
                                    >
                                        <SelectTrigger className="w-56">
                                            <SelectValue placeholder="Choose a session" />
                                        </SelectTrigger>
                                        <SelectContent>
                                            {createSessionOptions.map(
                                                (session) => (
                                                    <SelectItem
                                                        key={session.id}
                                                        value={session.id}
                                                    >
                                                        {session.label}
                                                        {session.phoneNumber
                                                            ? ` (${session.phoneNumber})`
                                                            : ''}
                                                    </SelectItem>
                                                ),
                                            )}
                                        </SelectContent>
                                    </Select>
                                </div>

                                <Button
                                    onClick={createPool}
                                    disabled={!createSender || !createSession}
                                >
                                    Create
                                </Button>
                            </div>

                            {createSender &&
                                createSessionOptions.length === 0 && (
                                    <p className="mt-2 text-sm text-muted-foreground">
                                        No eligible sessions available — every
                                        session already belongs to a pool.
                                    </p>
                                )}
                        </CardContent>
                    </Card>
                )}

                {pooledSenders.map((sender) => {
                    const members = pools[sender.name] ?? [];
                    const hasMain = members.some((m) => m.is_main);
                    const availableSessions = sessions.filter(
                        (s) => !sessionsInAnyPool(pools).has(s.id),
                    );

                    return (
                        <Card key={sender.name}>
                            <CardHeader className="flex flex-row items-center justify-between">
                                <CardTitle>{sender.name}</CardTitle>
                                <Button
                                    size="sm"
                                    variant="ghost"
                                    className="text-destructive hover:text-destructive"
                                    onClick={() => doDeletePool(sender.name)}
                                >
                                    Delete pool
                                </Button>
                            </CardHeader>
                            <CardContent>
                                <div className="flex flex-col gap-4">
                                    {!hasMain && (
                                        <p className="text-sm text-destructive">
                                            No session is currently eligible —
                                            sends for {sender.name} are being
                                            rejected until a backup is
                                            reinstated or promoted.
                                        </p>
                                    )}

                                    <div className="overflow-x-auto">
                                        <table className="w-full text-sm">
                                            <thead>
                                                <tr className="border-b text-left">
                                                    <th className="p-2 font-medium">
                                                        Session
                                                    </th>
                                                    <th className="p-2 font-medium">
                                                        Status
                                                    </th>
                                                    <th className="p-2 font-medium">
                                                        Actions
                                                    </th>
                                                </tr>
                                            </thead>
                                            <tbody>
                                                {members.map((member) => (
                                                    <tr
                                                        key={member.session_id}
                                                        className="border-b last:border-0"
                                                    >
                                                        <td className="p-2">
                                                            {sessionLabel(
                                                                sessions,
                                                                member.session_id,
                                                            )}
                                                        </td>
                                                        <td className="p-2">
                                                            <MemberBadge
                                                                member={member}
                                                            />
                                                        </td>
                                                        <td className="p-2">
                                                            <div className="flex flex-wrap gap-2">
                                                                {!member.is_main &&
                                                                    !member.disqualified && (
                                                                        <Button
                                                                            size="sm"
                                                                            variant="outline"
                                                                            onClick={() =>
                                                                                doPromote(
                                                                                    sender.name,
                                                                                    member.session_id,
                                                                                )
                                                                            }
                                                                        >
                                                                            Promote
                                                                        </Button>
                                                                    )}
                                                                {member.disqualified && (
                                                                    <Button
                                                                        size="sm"
                                                                        variant="outline"
                                                                        onClick={() =>
                                                                            doReinstate(
                                                                                sender.name,
                                                                                member.session_id,
                                                                            )
                                                                        }
                                                                    >
                                                                        Reinstate
                                                                    </Button>
                                                                )}
                                                                {!member.is_main && (
                                                                    <Button
                                                                        size="sm"
                                                                        variant="destructive"
                                                                        onClick={() =>
                                                                            doRemove(
                                                                                sender.name,
                                                                                member.session_id,
                                                                            )
                                                                        }
                                                                    >
                                                                        Remove
                                                                    </Button>
                                                                )}
                                                            </div>
                                                        </td>
                                                    </tr>
                                                ))}
                                            </tbody>
                                        </table>
                                    </div>

                                    <Button
                                        size="sm"
                                        variant="outline"
                                        className="self-start"
                                        onClick={() =>
                                            setAddingBackupFor(sender.name)
                                        }
                                    >
                                        Add backup
                                    </Button>
                                </div>
                            </CardContent>

                            {addingBackupFor === sender.name && (
                                <SessionPickerDialog
                                    open
                                    onOpenChange={(open) =>
                                        !open && setAddingBackupFor(null)
                                    }
                                    title={`Add a backup session to ${sender.name}`}
                                    sessions={availableSessions}
                                    submitLabel="Add"
                                    onSubmit={(sessionId) =>
                                        addBackup(sender.name, sessionId)
                                    }
                                />
                            )}
                        </Card>
                    );
                })}

                {pooledSenders.length === 0 && poolableSenders.length === 0 && (
                    <p className="text-sm text-muted-foreground">
                        No senders configured.
                    </p>
                )}
            </div>
        </>
    );
}

Pools.layout = {
    breadcrumbs: [
        {
            title: 'Pools',
            href: poolsRoute(),
        },
    ],
};
