import { User } from '../../User.ts'
import { Request, Response } from '../../../framework.ts'

/**
 * Defines the MainController.
 *
 * @export
 * @controller MainController
 */
export namespace MainController {

    /**
     * Index page for the controller.
     *
     * @export
     * @returns {Response}
     */
    export function index(): Response {
        return `
            <h1>GET Form example</h1>
            <form action="/data" method="GET">
                <input type="text" name="name" placeholder="name"><br>
                <input type="checkbox" name="tos"> Accept TOS<br>
                <input type="text" name="balance" placeholder="balance"><br>
                <input type="submit">
            </form>
            <h1>POST Form example</h1>
            <form action="/data" method="POST">
                <input type="text" name="name" placeholder="name"><br>
                <input type="checkbox" name="tos"> Accept TOS<br>
                <input type="text" name="balance" placeholder="balance"><br>
                <input type="submit">
            </form>
        `
    }

    /**
     * Example data to post to.
     *
     * @export
     * @param {Request} { request }
     * @returns {Response}
     */
    export function data({ request }: Request): Response {
        const attributes = request.validate({
            'name': 'required',
            'tos': 'accepted',
            'balance': 'required|numeric|min:20|max:100'
        })
        return { method: request.method(), ...attributes }
    }

    /**
     * Another page for the controller.
     *
     * @export
     * @returns {Response}
     */
    export function another(): Response {
        return { table: (new User).getTable() }
    }

    /**
     * A page with two parameters.
     *
     * @export
     * @param {Request} { params }
     * @returns {Response}
     */
    export function book({ params }: Request): Response {
        return `<h1>Book: ${params.name}, page ${params.page}</h1>`
    }

}
