import { DropdownMenu } from '@radix-ui/themes'
import { ChevronDown, LayoutGrid, LogOut, UserRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { useAuth } from '../model/use-auth'
import styles from './auth-modal.module.scss'

function initials(name: string) {
  const words = name
    .replace(/@.*$/, '')
    .split(/[\s._-]+/)
    .filter(Boolean)
  return (
    words
      .slice(0, 2)
      .map((word) => word[0])
      .join('')
      .toUpperCase() || '?'
  )
}

// canLogout=false shows only the profile, without the menu: the admin panel
// is a shared account that enters by itself, so leaving it makes no sense.
export function AccountMenu({ canLogout = true }: { canLogout?: boolean }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { user, logout } = useAuth()
  // Accounts created before the name became required fall back to email.
  const name = user?.display_name || user?.email || ''
  if (!canLogout)
    return (
      <div className={styles.account}>
        <span
          className={`${styles.profile} ${styles.profileStatic}`}
          aria-label={`${t('auth.account')}: ${name}`}
        >
          <span className={styles.avatar} aria-hidden="true">
            {initials(name)}
          </span>
          <span className={styles.user}>{name}</span>
        </span>
      </div>
    )
  return (
    <div className={styles.account}>
      <DropdownMenu.Root>
        <DropdownMenu.Trigger>
          <button
            type="button"
            className={styles.profile}
            aria-label={`${t('auth.accountMenu')}: ${name}`}
          >
            <span className={styles.avatar} aria-hidden="true">
              {initials(name)}
            </span>
            <span className={styles.user}>{name}</span>
            <ChevronDown size={16} aria-hidden="true" />
          </button>
        </DropdownMenu.Trigger>
        <DropdownMenu.Content align="end" sideOffset={6}>
          <DropdownMenu.Item onSelect={() => void navigate({ to: '/profile' })}>
            <UserRound size={16} aria-hidden="true" />
            {t('profile.title')}
          </DropdownMenu.Item>
          <DropdownMenu.Item onSelect={() => void navigate({ to: '/' })}>
            <LayoutGrid size={16} aria-hidden="true" />
            {t('auth.modeSelect')}
          </DropdownMenu.Item>
          <DropdownMenu.Item
            color="red"
            disabled={logout.isPending}
            onSelect={() => logout.mutate()}
          >
            <LogOut size={16} aria-hidden="true" />
            {t('auth.logout')}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Root>
      {logout.isError && (
        <span role="alert" className={styles.error}>
          {t('auth.networkError')}
        </span>
      )}
    </div>
  )
}
