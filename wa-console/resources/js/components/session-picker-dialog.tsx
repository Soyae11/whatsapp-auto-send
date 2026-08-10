import { useState } from 'react';
import { Button } from '@/components/ui/button';
import {
    Dialog,
    DialogContent,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import type { PoolSession } from '@/types/pool';

export function SessionPickerDialog({
    open,
    onOpenChange,
    title,
    sessions,
    onSubmit,
    submitLabel,
}: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    title: string;
    sessions: PoolSession[];
    onSubmit: (sessionId: string) => void;
    submitLabel: string;
}) {
    const [sessionId, setSessionId] = useState('');

    function submit() {
        if (!sessionId) {
            return;
        }

        onSubmit(sessionId);
        setSessionId('');
    }

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent>
                <DialogHeader>
                    <DialogTitle>{title}</DialogTitle>
                </DialogHeader>

                {sessions.length === 0 ? (
                    <p className="text-sm text-muted-foreground">
                        No eligible sessions available.
                    </p>
                ) : (
                    <Select value={sessionId} onValueChange={setSessionId}>
                        <SelectTrigger>
                            <SelectValue placeholder="Choose a session" />
                        </SelectTrigger>
                        <SelectContent>
                            {sessions.map((session) => (
                                <SelectItem key={session.id} value={session.id}>
                                    {session.label}
                                    {session.phoneNumber
                                        ? ` (${session.phoneNumber})`
                                        : ''}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                )}

                <DialogFooter>
                    <Button onClick={submit} disabled={!sessionId}>
                        {submitLabel}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
