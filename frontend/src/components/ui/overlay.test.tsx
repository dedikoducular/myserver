import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expectAccessibleNames } from '@/test/a11y'
import { deferred } from '@/test/helpers'
import { ConfirmDialog, Menu, Modal, type ConfirmDialogProps } from './overlay'

afterEach(() => {
  document.body.style.overflow = ''
})

function backdrop(): HTMLElement {
  return screen.getByRole('dialog').previousElementSibling as HTMLElement
}

function ModalHarness({ busy = false, onClose }: { busy?: boolean; onClose?: () => void }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button onClick={() => setOpen(true)}>Aç</button>
      <button>Arka plandaki düğme</button>
      <Modal
        open={open}
        busy={busy}
        onClose={() => {
          onClose?.()
          setOpen(false)
        }}
        title="Disk ayrıntıları"
        description="sda"
        footer={<button>Tamam</button>}
      >
        <input aria-label="Ad" />
        <button disabled>Devre dışı</button>
      </Modal>
    </>
  )
}

describe('Modal', () => {
  it('kapalıyken hiçbir şey göstermez', () => {
    render(<ModalHarness />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('başlığıyla adlandırılmış bir iletişim kutusu açar ve odağı içine taşır', async () => {
    const user = userEvent.setup()
    render(<ModalHarness />)
    await user.click(screen.getByRole('button', { name: 'Aç' }))
    const dialog = screen.getByRole('dialog', { name: 'Disk ayrıntıları' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
    expectAccessibleNames(dialog)
  })

  it('data-autofocus verilen öğeye odaklanır', async () => {
    render(
      <Modal open onClose={() => undefined} title="Başlık">
        <input aria-label="Birinci" />
        <input aria-label="İkinci" data-autofocus />
      </Modal>,
    )
    expect(screen.getByLabelText('İkinci')).toHaveFocus()
  })

  it('Tab ile odak iletişim kutusunun dışına çıkmaz', async () => {
    const user = userEvent.setup()
    render(<ModalHarness />)
    await user.click(screen.getByRole('button', { name: 'Aç' }))
    const dialog = screen.getByRole('dialog')
    const close = screen.getByRole('button', { name: 'Kapat' })
    const last = screen.getByRole('button', { name: 'Tamam' })

    expect(close).toHaveFocus()
    await user.tab()
    expect(screen.getByLabelText('Ad')).toHaveFocus()
    await user.tab()
    expect(last).toHaveFocus()
    await user.tab()
    expect(close).toHaveFocus()
    await user.tab({ shift: true })
    expect(last).toHaveFocus()
    for (let i = 0; i < 6; i++) {
      await user.tab()
      expect(dialog).toContainElement(document.activeElement as HTMLElement)
    }
  })

  it('odaklanabilir öğesi olmayan kutuda Tab odağı dışarı kaçırmaz', async () => {
    const user = userEvent.setup()
    function Bare() {
      const [busy] = useState(true)
      return (
        <>
          <button>Dışarıda</button>
          <Modal open busy={busy} onClose={() => undefined} title="Bekleyin" />
        </>
      )
    }
    render(<Bare />)
    await user.tab()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Dışarıda' })).not.toHaveFocus()
  })

  it('Escape ile kapanır ve odak açan düğmeye döner', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(<ModalHarness onClose={onClose} />)
    const trigger = screen.getByRole('button', { name: 'Aç' })
    await user.click(trigger)
    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('arka plana tıklanınca ve Kapat düğmesiyle kapanır', async () => {
    const user = userEvent.setup()
    render(<ModalHarness />)
    await user.click(screen.getByRole('button', { name: 'Aç' }))
    await user.click(backdrop())
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Aç' }))
    await user.click(screen.getByRole('button', { name: 'Kapat' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('kutunun içine tıklamak kapatmaz', async () => {
    const user = userEvent.setup()
    render(<ModalHarness />)
    await user.click(screen.getByRole('button', { name: 'Aç' }))
    await user.click(screen.getByLabelText('Ad'))
    await user.click(screen.getByText('Disk ayrıntıları'))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('meşgulken Escape, arka plan ve Kapat düğmesiyle kapatılamaz', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(<ModalHarness busy onClose={onClose} />)
    await user.click(screen.getByRole('button', { name: 'Aç' }))
    await user.keyboard('{Escape}')
    await user.click(backdrop())
    expect(screen.getByRole('button', { name: 'Kapat' })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('arka plan kaydırmasını kilitler ve kapanınca eski değeri geri yükler', async () => {
    const user = userEvent.setup()
    document.body.style.overflow = 'scroll'
    render(<ModalHarness />)
    await user.click(screen.getByRole('button', { name: 'Aç' }))
    expect(document.body.style.overflow).toBe('hidden')
    await user.keyboard('{Escape}')
    expect(document.body.style.overflow).toBe('scroll')
  })

  it('açıkken kaldırılırsa kaydırma kilidini bırakır', () => {
    const { unmount } = render(<Modal open onClose={() => undefined} title="x" />)
    expect(document.body.style.overflow).toBe('hidden')
    unmount()
    expect(document.body.style.overflow).toBe('')
  })
})

function ConfirmHarness(props: Partial<ConfirmDialogProps> & { onClosed?: () => void }) {
  const [open, setOpen] = useState(true)
  const { onClosed, ...rest } = props
  return (
    <ConfirmDialog
      open={open}
      onClose={() => {
        onClosed?.()
        setOpen(false)
      }}
      onConfirm={() => undefined}
      title="Disk biçimlendirilsin mi?"
      message="sdb üzerindeki tüm veriler silinecek."
      confirmLabel="Biçimlendir"
      danger
      {...rest}
    />
  )
}

describe('ConfirmDialog', () => {
  it('başlığı, mesajı ve uyarıyı gösterir; onay verilince işlemi çalıştırır', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<ConfirmHarness onConfirm={onConfirm} warning="Bu işlem geri alınamaz." />)
    const dialog = screen.getByRole('dialog', { name: 'Disk biçimlendirilsin mi?' })
    expect(dialog).toHaveTextContent('sdb üzerindeki tüm veriler silinecek.')
    expect(dialog).toHaveTextContent('Bu işlem geri alınamaz.')
    expectAccessibleNames(dialog)
    await user.click(screen.getByRole('button', { name: 'Biçimlendir' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('odak yıkıcı düğmeye değil Vazgeç düğmesine gelir', () => {
    render(<ConfirmHarness />)
    expect(screen.getByRole('button', { name: 'Vazgeç' })).toHaveFocus()
  })

  it('Vazgeç işlemi çalıştırmadan kapatır', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    const onClosed = vi.fn()
    render(<ConfirmHarness onConfirm={onConfirm} onClosed={onClosed} />)
    await user.click(screen.getByRole('button', { name: 'Vazgeç' }))
    expect(onConfirm).not.toHaveBeenCalled()
    expect(onClosed).toHaveBeenCalledTimes(1)
  })

  describe('requireText', () => {
    const setup = () => {
      const user = userEvent.setup()
      const onConfirm = vi.fn()
      render(<ConfirmHarness onConfirm={onConfirm} requireText="sdb" />)
      const input = screen.getByRole('textbox')
      const button = screen.getByRole('button', { name: 'Biçimlendir' })
      return { user, onConfirm, input, button }
    }

    it('metin alanının erişilebilir adı yazılacak metni içerir', () => {
      const { input } = setup()
      expect(input).toHaveAccessibleName('Onaylamak için sdb yazın')
    })

    it('metin yazılmadan onay düğmesi devre dışıdır', () => {
      const { button } = setup()
      expect(button).toBeDisabled()
    })

    it.each([
      ['büyük harf', 'SDB'],
      ['karışık harf', 'Sdb'],
      ['eksik karakter', 'sd'],
      ['fazla karakter', 'sdb1'],
      ['başta fazla karakter', 'xsdb'],
      ['içinde boşluk', 's db'],
      ['iki kez', 'sdb sdb'],
      ['tırnak içinde', '"sdb"'],
    ])('yakın ama yanlış metinde (%s) devre dışı kalır', async (_name, text) => {
      const { user, input, button, onConfirm } = setup()
      await user.type(input, text)
      expect(button).toBeDisabled()
      await user.click(button)
      expect(onConfirm).not.toHaveBeenCalled()
    })

    it('tam metin yazılınca etkinleşir; metin bozulunca yeniden devre dışı kalır', async () => {
      const { user, input, button } = setup()
      await user.type(input, 'sdb')
      expect(button).toBeEnabled()
      await user.type(input, 'x')
      expect(button).toBeDisabled()
      await user.type(input, '{Backspace}')
      expect(button).toBeEnabled()
    })

    it('baştaki ve sondaki boşluklar yok sayılır', async () => {
      const { user, input, button } = setup()
      await user.type(input, '  sdb ')
      expect(button).toBeEnabled()
    })

    it('Türkçe büyük/küçük harf farkını eşleşme saymaz', async () => {
      const user = userEvent.setup()
      const onConfirm = vi.fn()
      render(<ConfirmHarness onConfirm={onConfirm} requireText="SİL" />)
      const button = screen.getByRole('button', { name: 'Biçimlendir' })
      for (const wrong of ['SIL', 'sil', 'Sil', 'SİL.']) {
        await user.clear(screen.getByRole('textbox'))
        await user.type(screen.getByRole('textbox'), wrong)
        expect(button).toBeDisabled()
      }
      await user.clear(screen.getByRole('textbox'))
      await user.type(screen.getByRole('textbox'), 'SİL')
      expect(button).toBeEnabled()
    })

    it('metin yanlışken Enter onaylamaz', async () => {
      const { user, input, onConfirm } = setup()
      await user.type(input, '{Enter}')
      await user.type(input, 'SDB{Enter}')
      await user.type(input, '1{Enter}')
      expect(onConfirm).not.toHaveBeenCalled()
      expect(screen.getByRole('dialog')).toBeInTheDocument()
    })

    it('metin doğruyken Enter bir kez onaylar', async () => {
      const { user, input, onConfirm } = setup()
      await user.type(input, 'sdb{Enter}')
      expect(onConfirm).toHaveBeenCalledTimes(1)
    })

    it('yeniden açıldığında yazılan metin temizlenir', async () => {
      const user = userEvent.setup()
      const props = { onClose: () => undefined, onConfirm: () => undefined, title: 'Sil', message: 'm', requireText: 'sdb' }
      const { rerender } = render(<ConfirmDialog open {...props} />)
      await user.type(screen.getByRole('textbox'), 'sdb')
      expect(screen.getByRole('button', { name: 'Onayla' })).toBeEnabled()
      rerender(<ConfirmDialog open={false} {...props} />)
      rerender(<ConfirmDialog open {...props} />)
      expect(screen.getByRole('textbox')).toHaveValue('')
      expect(screen.getByRole('button', { name: 'Onayla' })).toBeDisabled()
    })
  })

  describe('işlem sürerken', () => {
    it('ilerleme gösterir, düğmeleri kilitler ve kapatılamaz', async () => {
      const user = userEvent.setup()
      const work = deferred<void>()
      const onConfirm = vi.fn(() => work.promise)
      const onClosed = vi.fn()
      render(<ConfirmHarness onConfirm={onConfirm} onClosed={onClosed} />)
      const confirm = screen.getByRole('button', { name: 'Biçimlendir' })
      await user.click(confirm)

      expect(confirm).toBeDisabled()
      expect(confirm.querySelector('.ms-spin')).not.toBeNull()
      expect(screen.getByRole('button', { name: 'Vazgeç' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Kapat' })).toBeDisabled()

      await user.keyboard('{Escape}')
      await user.click(backdrop())
      await user.click(confirm)
      expect(onClosed).not.toHaveBeenCalled()
      expect(onConfirm).toHaveBeenCalledTimes(1)
      expect(screen.getByRole('dialog')).toBeInTheDocument()

      work.resolve()
      await waitFor(() => expect(confirm).toBeEnabled())
    })

    it('Enter ile art arda onay işlemi iki kez başlatmaz', async () => {
      const user = userEvent.setup()
      const work = deferred<void>()
      const onConfirm = vi.fn(() => work.promise)
      render(<ConfirmHarness onConfirm={onConfirm} requireText="sdb" />)
      await user.type(screen.getByRole('textbox'), 'sdb{Enter}{Enter}{Enter}')
      expect(onConfirm).toHaveBeenCalledTimes(1)
      work.resolve()
    })

    it('pending verildiğinde de kapatılamaz', async () => {
      const user = userEvent.setup()
      const onClosed = vi.fn()
      render(<ConfirmHarness pending onClosed={onClosed} />)
      await user.keyboard('{Escape}')
      await user.click(backdrop())
      expect(onClosed).not.toHaveBeenCalled()
      expect(screen.getByRole('button', { name: 'Biçimlendir' })).toBeDisabled()
    })
  })

  describe('işlem başarısız olduğunda', () => {
    it('error ile verilen mesajı role="alert" içinde gösterir ve açık kalır', () => {
      render(<ConfirmHarness error="Disk biçimlendirilemedi." />)
      expect(screen.getByRole('alert')).toHaveTextContent('Disk biçimlendirilemedi.')
      expect(screen.getByRole('dialog')).toBeInTheDocument()
    })

    it('işlem hata fırlatırsa açık kalır, hatayı gösterir ve yeniden denenebilir', async () => {
      const user = userEvent.setup()
      const onClosed = vi.fn()
      const onConfirm = vi.fn().mockRejectedValueOnce(new Error('Disk kullanımda.')).mockResolvedValueOnce(undefined)
      render(<ConfirmHarness onConfirm={onConfirm} onClosed={onClosed} />)
      const confirm = screen.getByRole('button', { name: 'Biçimlendir' })
      await user.click(confirm)

      expect(await screen.findByRole('alert')).toHaveTextContent('Disk kullanımda.')
      expect(screen.getByRole('dialog')).toBeInTheDocument()
      expect(onClosed).not.toHaveBeenCalled()
      expect(confirm).toBeEnabled()

      await user.click(confirm)
      expect(onConfirm).toHaveBeenCalledTimes(2)
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    })
  })
})

describe('Menu', () => {
  it('adlandırılmış düğmeyle açılır, seçimi çalıştırır ve kapanır', async () => {
    const user = userEvent.setup()
    const onRestart = vi.fn()
    const onDelete = vi.fn()
    render(
      <Menu
        label="nginx işlemleri"
        trigger={<span aria-hidden>…</span>}
        items={[
          { label: 'Yeniden Başlat', onSelect: onRestart },
          { label: 'Sil', onSelect: onDelete, danger: true, disabled: true },
        ]}
      />,
    )
    const trigger = screen.getByRole('button', { name: 'nginx işlemleri' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await user.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('menuitem', { name: 'Sil' })).toBeDisabled()
    await user.click(screen.getByRole('menuitem', { name: 'Yeniden Başlat' }))
    expect(onRestart).toHaveBeenCalledTimes(1)
    expect(onDelete).not.toHaveBeenCalled()
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('Escape ile kapanır ve odak düğmeye döner', async () => {
    const user = userEvent.setup()
    render(<Menu label="İşlemler" trigger="…" items={[{ label: 'Aç', onSelect: () => undefined }]} />)
    await user.click(screen.getByRole('button', { name: 'İşlemler' }))
    expect(screen.getByRole('menuitem', { name: 'Aç' })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'İşlemler' })).toHaveFocus()
  })
})
