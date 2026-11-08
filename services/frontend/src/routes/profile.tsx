import { createFileRoute } from '@tanstack/react-router'
import { ProfileScreen } from '@/pages/profile'

export const Route = createFileRoute('/profile')({ component: ProfileScreen })
