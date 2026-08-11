import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { show } from '@/actions/App/Http/Controllers/MessagesController';
import { Badge } from '@/components/ui/badge';
import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import { Spinner } from '@/components/ui/spinner';
import { apiFetch } from '@/lib/http';
import { sessionLabel } from '@/lib/session-label';
import type { Message, MessageStatus } from '@/types/message';
import type { PoolSession } from '@/types/pool';

const STATUS_VARIANT: Record<
    MessageStatus,
    'default' | 'secondary' | 'destructive' | 'outline'
> = {
    queued: 'secondary',
    sending: 'secondary',
    sent: 'default',
    delivered: 'default',
    read: 'default',
    cancelled: 'outline',
    failed: 'destructive',
};

const TIMELINE_STEPS: { key: keyof Message['timestamps']; label: string }[] = [
    { key: 'queued_at', label: 'Queued' },
    { key: 'sending_at', label: 'Sending' },
    { key: 'sent_at', label: 'Sent' },
    { key: 'delivered_at', label: 'Delivered' },
    { key: 'read_at', label: 'Read' },
    { key: 'cancelled_at', label: 'Cancelled' },
    { key: 'failed_at', label: 'Failed' },
];

export function MessageDetailDialog({
    id,
    sessions,
    open,
    onOpenChange,
}: {
    id: string;
    sessions: PoolSession[];
    open: boolean;
    onOpenChange: (open: boolean) => void;
}) {
    const [message, setMessage] = useState<Message | null>(null);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        if (!open) {
            return;
        }

        let cancelled = false;

        apiFetch<Message>(show.url(id))
            .then((data) => {
                if (!cancelled) {
                    setMessage(data);
                }
            })
            .catch((error) => {
                if (!cancelled) {
                    toast.error(
                        error instanceof Error
                            ? error.message
                            : 'Failed to load message.',
                    );
                    onOpenChange(false);
                }
            })
            .finally(() => {
                if (!cancelled) {
                    setLoading(false);
                }
            });

        return () => {
            cancelled = true;
        };
    }, [open, id, onOpenChange]);

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent className="max-w-lg">
                <DialogHeader>
                    <DialogTitle>{id}</DialogTitle>
                </DialogHeader>

                {loading || !message ? (
                    <div className="flex justify-center py-8">
                        <Spinner className="size-6" />
                    </div>
                ) : (
                    <div className="flex flex-col gap-4">
                        <div className="grid grid-cols-2 gap-2 text-sm">
                            <span className="text-muted-foreground">
                                Status
                            </span>
                            <Badge
                                variant={STATUS_VARIANT[message.status]}
                                className="w-fit"
                            >
                                {message.status}
                            </Badge>

                            <span className="text-muted-foreground">To</span>
                            <span>{message.to}</span>

                            <span className="text-muted-foreground">
                                Sender
                            </span>
                            <span>{message.sender}</span>

                            <span className="text-muted-foreground">
                                Session
                            </span>
                            <span>
                                {message.session_id
                                    ? sessionLabel(
                                          sessions,
                                          message.session_id,
                                      )
                                    : '—'}
                            </span>

                            <span className="text-muted-foreground">
                                Priority
                            </span>
                            <span>{message.priority}</span>

                            <span className="text-muted-foreground">
                                Attempts
                            </span>
                            <span>{message.attempts}</span>

                            <span className="text-muted-foreground">
                                Dry run
                            </span>
                            <span>{message.dry_run ? 'Yes' : 'No'}</span>

                            {message.reference && (
                                <>
                                    <span className="text-muted-foreground">
                                        Reference
                                    </span>
                                    <span>{message.reference}</span>
                                </>
                            )}

                            {message.failover_of && (
                                <>
                                    <span className="text-muted-foreground">
                                        Retry of
                                    </span>
                                    <span className="font-mono text-xs">
                                        {message.failover_of}
                                    </span>
                                </>
                            )}

                            {message.retried_by && (
                                <>
                                    <span className="text-muted-foreground">
                                        Retried as
                                    </span>
                                    <span className="font-mono text-xs">
                                        {message.retried_by}
                                    </span>
                                </>
                            )}
                        </div>

                        <div>
                            <p className="mb-2 text-sm font-medium">Timeline</p>
                            <div className="flex flex-col gap-1">
                                {TIMELINE_STEPS.filter(
                                    (step) => message.timestamps[step.key],
                                ).map((step) => (
                                    <div
                                        key={step.key}
                                        className="flex justify-between text-sm"
                                    >
                                        <span className="text-muted-foreground">
                                            {step.label}
                                        </span>
                                        <span>
                                            {new Date(
                                                message.timestamps[step.key]!,
                                            ).toLocaleString()}
                                        </span>
                                    </div>
                                ))}
                            </div>
                        </div>

                        {message.last_error && (
                            <div className="rounded-md border border-destructive/50 p-3 text-sm">
                                <p className="font-medium text-destructive">
                                    {message.last_error.code}
                                </p>
                                <p className="mt-1 text-muted-foreground">
                                    {message.last_error.detail}
                                </p>
                                <p className="mt-1 text-muted-foreground">
                                    {message.last_error.retryable
                                        ? 'Retryable'
                                        : 'Not retryable'}
                                </p>
                            </div>
                        )}

                        {message.metadata &&
                            Object.keys(message.metadata).length > 0 && (
                                <div>
                                    <p className="mb-2 text-sm font-medium">
                                        Metadata
                                    </p>
                                    <div className="flex flex-col gap-1">
                                        {Object.entries(message.metadata).map(
                                            ([key, value]) => (
                                                <div
                                                    key={key}
                                                    className="flex justify-between text-sm"
                                                >
                                                    <span className="text-muted-foreground">
                                                        {key}
                                                    </span>
                                                    <span>{value}</span>
                                                </div>
                                            ),
                                        )}
                                    </div>
                                </div>
                            )}
                    </div>
                )}
            </DialogContent>
        </Dialog>
    );
}
