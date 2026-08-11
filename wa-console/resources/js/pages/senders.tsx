import { Head, router } from '@inertiajs/react';
import { useState } from 'react';
import { destroy, store } from '@/actions/App/Http/Controllers/SendersController';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { senders as sendersRoute } from '@/routes';
import type { OwnedSender, SenderMode } from '@/types/sender';

type SenderSession = {
    id: string;
    label: string;
    phoneNumber: string | null;
};

export default function Senders({
    senders,
    sessions,
}: {
    senders: OwnedSender[];
    sessions: SenderSession[];
}) {
    const [name, setName] = useState('');
    const [mode, setMode] = useState<SenderMode>('single');
    const [sessionId, setSessionId] = useState('');

    function create() {
        if (!name || (mode === 'single' && !sessionId)) {
            return;
        }

        router.post(
            store.url(),
            { name, mode, session_id: mode === 'single' ? sessionId : '' },
            { onSuccess: () => setName('') },
        );
        setSessionId('');
    }

    function remove(sender: string) {
        if (window.confirm(`Remove sender "${sender}"? Any pool it has is deleted too.`)) {
            router.delete(destroy.url({ sender }));
        }
    }

    return (
        <>
            <Head title="Senders" />
            <div className="flex flex-1 flex-col gap-4 p-4">
                <Card>
                    <CardHeader>
                        <CardTitle>New sender</CardTitle>
                    </CardHeader>
                    <CardContent>
                        <div className="flex flex-wrap items-end gap-4">
                            <div className="flex flex-col gap-2">
                                <span className="text-sm font-medium">Name</span>
                                <Input
                                    className="w-56"
                                    placeholder="e.g. sales-team"
                                    value={name}
                                    onChange={(event) => setName(event.target.value)}
                                />
                            </div>

                            <div className="flex flex-col gap-2">
                                <span className="text-sm font-medium">Mode</span>
                                <Select
                                    value={mode}
                                    onValueChange={(value) => {
                                        setMode(value as SenderMode);
                                        setSessionId('');
                                    }}
                                >
                                    <SelectTrigger className="w-56">
                                        <SelectValue />
                                    </SelectTrigger>
                                    <SelectContent>
                                        <SelectItem value="single">
                                            Single session
                                        </SelectItem>
                                        <SelectItem value="pool">
                                            Pool (add sessions after)
                                        </SelectItem>
                                    </SelectContent>
                                </Select>
                            </div>

                            {mode === 'single' && (
                                <div className="flex flex-col gap-2">
                                    <span className="text-sm font-medium">
                                        Session
                                    </span>
                                    <Select
                                        value={sessionId}
                                        onValueChange={setSessionId}
                                    >
                                        <SelectTrigger className="w-56">
                                            <SelectValue placeholder="Choose a session" />
                                        </SelectTrigger>
                                        <SelectContent>
                                            {sessions.map((session) => (
                                                <SelectItem
                                                    key={session.id}
                                                    value={session.id}
                                                >
                                                    {session.label}
                                                    {session.phoneNumber
                                                        ? ` (${session.phoneNumber})`
                                                        : ''}
                                                </SelectItem>
                                            ))}
                                        </SelectContent>
                                    </Select>
                                </div>
                            )}

                            <Button
                                onClick={create}
                                disabled={
                                    !name ||
                                    (mode === 'single' && !sessionId)
                                }
                            >
                                Create
                            </Button>
                        </div>

                        {sessions.length === 0 && (
                            <p className="mt-2 text-sm text-muted-foreground">
                                No paired sessions yet — pair one on the
                                Sessions page first if you want a single-mode
                                sender.
                            </p>
                        )}
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>Your senders</CardTitle>
                    </CardHeader>
                    <CardContent>
                        {senders.length === 0 ? (
                            <p className="text-sm text-muted-foreground">
                                No senders yet. Create one above, then generate
                                an API key for it on the API Keys page.
                            </p>
                        ) : (
                            <div className="overflow-x-auto">
                                <table className="w-full text-sm">
                                    <thead>
                                        <tr className="border-b text-left">
                                            <th className="p-2 font-medium">
                                                Name
                                            </th>
                                            <th className="p-2 font-medium">
                                                Mode
                                            </th>
                                            <th className="p-2 font-medium">
                                                Session
                                            </th>
                                            <th className="p-2 font-medium">
                                                Actions
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {senders.map((sender) => (
                                            <tr
                                                key={sender.name}
                                                className="border-b last:border-0"
                                            >
                                                <td className="p-2">
                                                    {sender.name}
                                                </td>
                                                <td className="p-2">
                                                    <Badge
                                                        variant={
                                                            sender.mode ===
                                                            'pool'
                                                                ? 'secondary'
                                                                : 'outline'
                                                        }
                                                    >
                                                        {sender.mode ===
                                                        'pool'
                                                            ? 'Pool'
                                                            : 'Session'}
                                                    </Badge>
                                                </td>
                                                <td className="p-2">
                                                    {sender.session_id ??
                                                        '—'}
                                                </td>
                                                <td className="p-2">
                                                    <Button
                                                        size="sm"
                                                        variant="destructive"
                                                        onClick={() =>
                                                            remove(
                                                                sender.name,
                                                            )
                                                        }
                                                    >
                                                        Remove
                                                    </Button>
                                                </td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </table>
                            </div>
                        )}
                    </CardContent>
                </Card>
            </div>
        </>
    );
}

Senders.layout = {
    breadcrumbs: [
        {
            title: 'Senders',
            href: sendersRoute(),
        },
    ],
};
