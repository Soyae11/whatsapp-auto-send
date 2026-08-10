import { Form, Head, Link, router } from '@inertiajs/react';
import { useState } from 'react';
import {
    destroy,
    logout,
    store,
} from '@/actions/App/Http/Controllers/SessionsController';
import { SessionPairDialog } from '@/components/session-pair-dialog';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Spinner } from '@/components/ui/spinner';
import { dashboard } from '@/routes';
import type { Session, SessionStatus } from '@/types/session';

const STATUS_VARIANT: Record<
    SessionStatus,
    'default' | 'secondary' | 'destructive' | 'outline'
> = {
    connected: 'default',
    pairing: 'secondary',
    new: 'secondary',
    disconnected: 'outline',
    logged_out: 'destructive',
    unhealthy: 'destructive',
};

export default function Dashboard({ sessions }: { sessions: Session[] }) {
    const [pairingSession, setPairingSession] = useState<Session | null>(null);

    function confirmDelete(session: Session) {
        if (
            window.confirm(
                `Remove session "${session.label}"? This deletes it from baileys.`,
            )
        ) {
            router.delete(destroy.url(session.id));
        }
    }

    return (
        <>
            <Head title="Sessions" />
            <div className="flex flex-1 flex-col gap-4 p-4">
                <Card>
                    <CardHeader>
                        <CardTitle>New session</CardTitle>
                    </CardHeader>
                    <CardContent>
                        <Form
                            {...store.form()}
                            resetOnSuccess
                            className="flex gap-2"
                        >
                            {({ processing, errors }) => (
                                <>
                                    <div className="flex-1">
                                        <Input
                                            name="label"
                                            placeholder="e.g. sales-team"
                                        />
                                        {errors.label && (
                                            <p className="mt-1 text-sm text-destructive">
                                                {errors.label}
                                            </p>
                                        )}
                                    </div>
                                    <Button type="submit" disabled={processing}>
                                        {processing ? (
                                            <Spinner className="size-4" />
                                        ) : (
                                            'Create'
                                        )}
                                    </Button>
                                </>
                            )}
                        </Form>
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>Sessions</CardTitle>
                    </CardHeader>
                    <CardContent>
                        {sessions.length === 0 ? (
                            <p className="text-sm text-muted-foreground">
                                No sessions yet. Create one above.
                            </p>
                        ) : (
                            <div className="overflow-x-auto">
                                <table className="w-full text-sm">
                                    <thead>
                                        <tr className="border-b text-left">
                                            <th className="p-2 font-medium">
                                                Label
                                            </th>
                                            <th className="p-2 font-medium">
                                                Status
                                            </th>
                                            <th className="p-2 font-medium">
                                                Phone number
                                            </th>
                                            <th className="p-2 font-medium">
                                                Updated
                                            </th>
                                            <th className="p-2 font-medium">
                                                Actions
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {sessions.map((session) => (
                                            <tr
                                                key={session.id}
                                                className="border-b last:border-0"
                                            >
                                                <td className="p-2">
                                                    {session.label}
                                                </td>
                                                <td className="p-2">
                                                    <Badge
                                                        variant={
                                                            STATUS_VARIANT[
                                                                session.status
                                                            ]
                                                        }
                                                    >
                                                        {session.status}
                                                    </Badge>
                                                </td>
                                                <td className="p-2">
                                                    {session.phoneNumber ?? '—'}
                                                </td>
                                                <td className="p-2">
                                                    {new Date(
                                                        session.updatedAt,
                                                    ).toLocaleString()}
                                                </td>
                                                <td className="p-2">
                                                    <div className="flex flex-wrap gap-2">
                                                        <Button
                                                            size="sm"
                                                            variant="outline"
                                                            onClick={() =>
                                                                setPairingSession(
                                                                    session,
                                                                )
                                                            }
                                                        >
                                                            Pair
                                                        </Button>
                                                        <Link
                                                            href={logout.url(
                                                                session.id,
                                                            )}
                                                            method="post"
                                                            as="button"
                                                            className="inline-flex h-8 items-center justify-center rounded-md border px-3 text-sm shadow-xs hover:bg-accent"
                                                        >
                                                            Logout
                                                        </Link>
                                                        <Button
                                                            size="sm"
                                                            variant="destructive"
                                                            onClick={() =>
                                                                confirmDelete(
                                                                    session,
                                                                )
                                                            }
                                                        >
                                                            Remove
                                                        </Button>
                                                    </div>
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

            {pairingSession && (
                <SessionPairDialog
                    key={pairingSession.id}
                    session={pairingSession}
                    open={pairingSession !== null}
                    onOpenChange={(open) => {
                        if (!open) {
                            setPairingSession(null);
                        }
                    }}
                />
            )}
        </>
    );
}

Dashboard.layout = {
    breadcrumbs: [
        {
            title: 'Sessions',
            href: dashboard(),
        },
    ],
};
