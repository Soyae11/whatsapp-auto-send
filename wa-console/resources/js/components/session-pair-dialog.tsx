import { router } from '@inertiajs/react';
import { useEffect, useState } from 'react';
import QRCode from 'react-qr-code';
import { toast } from 'sonner';
import {
    connect,
    pair,
    qr as qrRoute,
} from '@/actions/App/Http/Controllers/SessionsController';
import { Button } from '@/components/ui/button';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Spinner } from '@/components/ui/spinner';
import { useClipboard } from '@/hooks/use-clipboard';
import { apiFetch } from '@/lib/http';
import type { Session, SessionStatus } from '@/types/session';

const POLL_INTERVAL_MS = 2000;

type PairMode = 'qr' | 'code';

export function SessionPairDialog({
    session,
    open,
    onOpenChange,
}: {
    session: Session;
    open: boolean;
    onOpenChange: (open: boolean) => void;
}) {
    const [mode, setMode] = useState<PairMode>('qr');
    const [status, setStatus] = useState<SessionStatus>(session.status);
    const [qrValue, setQrValue] = useState<string | null>(null);
    const [phoneNumber, setPhoneNumber] = useState('');
    const [pairingCode, setPairingCode] = useState<string | null>(null);
    const [requesting, setRequesting] = useState(false);
    const [copiedText, copy] = useClipboard();

    useEffect(() => {
        if (!open) {
            return;
        }

        let cancelled = false;
        let timer: ReturnType<typeof setInterval> | undefined;

        async function poll() {
            try {
                const data = await apiFetch<{
                    status: SessionStatus;
                    qr: string | null;
                }>(qrRoute.url(session.id));

                if (cancelled) {
                    return;
                }

                setStatus(data.status);
                setQrValue(data.qr);

                if (data.status === 'connected') {
                    toast.success(`Session "${session.label}" paired.`);
                    router.reload({ only: ['sessions'] });
                    onOpenChange(false);
                }
            } catch (error) {
                if (!cancelled) {
                    toast.error(
                        error instanceof Error
                            ? error.message
                            : 'Failed to check pairing status.',
                    );
                }
            }
        }

        async function start() {
            try {
                await apiFetch(connect.url(session.id), { method: 'post' });
            } catch (error) {
                if (!cancelled) {
                    toast.error(
                        error instanceof Error
                            ? error.message
                            : 'Failed to start pairing.',
                    );
                }

                return;
            }

            await poll();

            if (cancelled) {
                return;
            }

            timer = setInterval(poll, POLL_INTERVAL_MS);
        }

        start();

        return () => {
            cancelled = true;

            if (timer) {
                clearInterval(timer);
            }
        };
    }, [open, session.id, session.label, session.status, onOpenChange]);

    async function submitPhoneNumber(event: React.FormEvent) {
        event.preventDefault();
        setRequesting(true);

        try {
            const data = await apiFetch<{ pairingCode: string }>(
                pair.url(session.id),
                {
                    method: 'post',
                    body: JSON.stringify({ phoneNumber }),
                },
            );
            setPairingCode(data.pairingCode);
        } catch (error) {
            toast.error(
                error instanceof Error
                    ? error.message
                    : 'Failed to request a pairing code.',
            );
        } finally {
            setRequesting(false);
        }
    }

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent>
                <DialogHeader>
                    <DialogTitle>Pair "{session.label}"</DialogTitle>
                    <DialogDescription>Status: {status}</DialogDescription>
                </DialogHeader>

                <div className="flex gap-2">
                    <Button
                        size="sm"
                        variant={mode === 'qr' ? 'default' : 'outline'}
                        onClick={() => setMode('qr')}
                    >
                        Scan QR
                    </Button>
                    <Button
                        size="sm"
                        variant={mode === 'code' ? 'default' : 'outline'}
                        onClick={() => setMode('code')}
                    >
                        Enter code
                    </Button>
                </div>

                {mode === 'qr' && (
                    <div className="flex flex-col items-center gap-4 py-4">
                        {qrValue ? (
                            <div className="rounded-md bg-white p-4">
                                <QRCode value={qrValue} size={220} />
                            </div>
                        ) : (
                            <div className="flex h-[252px] w-[252px] items-center justify-center">
                                <Spinner className="size-8" />
                            </div>
                        )}
                        <p className="text-sm text-muted-foreground">
                            Open WhatsApp on your phone, go to Linked Devices,
                            and scan this code.
                        </p>
                    </div>
                )}

                {mode === 'code' && (
                    <div className="flex flex-col gap-4 py-4">
                        <form
                            onSubmit={submitPhoneNumber}
                            className="flex flex-col gap-2"
                        >
                            <Label htmlFor="phoneNumber">Phone number</Label>
                            <div className="flex gap-2">
                                <Input
                                    id="phoneNumber"
                                    placeholder="62812xxxxxxx"
                                    value={phoneNumber}
                                    onChange={(event) =>
                                        setPhoneNumber(event.target.value)
                                    }
                                />
                                <Button
                                    type="submit"
                                    disabled={
                                        requesting || phoneNumber.length === 0
                                    }
                                >
                                    {requesting ? (
                                        <Spinner className="size-4" />
                                    ) : (
                                        'Get code'
                                    )}
                                </Button>
                            </div>
                        </form>

                        {pairingCode && (
                            <div className="flex items-center justify-between rounded-md border p-4">
                                <span className="font-mono text-2xl tracking-widest">
                                    {pairingCode}
                                </span>
                                <Button
                                    size="sm"
                                    variant="outline"
                                    onClick={() => copy(pairingCode)}
                                >
                                    {copiedText === pairingCode
                                        ? 'Copied'
                                        : 'Copy'}
                                </Button>
                            </div>
                        )}

                        <p className="text-sm text-muted-foreground">
                            Open WhatsApp on your phone, go to Linked Devices →
                            Link with phone number, and enter this code.
                        </p>
                    </div>
                )}
            </DialogContent>
        </Dialog>
    );
}
